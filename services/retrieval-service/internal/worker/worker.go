// Package worker composes the portable local data-preparation adapter.
package worker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/belLena81/raglibrarian/pkg/contracts"
	"github.com/belLena81/raglibrarian/services/retrieval-service/config"
	"github.com/belLena81/raglibrarian/services/retrieval-service/diagnostic"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/application"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/artifact"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/domain"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/embedding"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/rabbitmq"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/repository"
	retrievalruntime "github.com/belLena81/raglibrarian/services/retrieval-service/internal/runtime"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/storage"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/transport"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rabbitmq/amqp091-go"
	"go.uber.org/zap"
)

const (
	metadataQueue  = contracts.QueueRetrievalMetadata
	manifestQueue  = contracts.QueueRetrievalManifest
	batchQueue     = contracts.QueueRetrievalIndex
	lifecycleQueue = contracts.QueueRetrievalLifecycle
	eventExchange  = contracts.ExchangeRetrievalEvents
	retryExchange  = contracts.ExchangeRetrievalRetry
)

var errManifestArtifactRead = errors.New("manifest artifact read failed")

type manifestFailureRecorder interface {
	FailManifest(context.Context, application.ManifestEvent, domain.FailureCategory, time.Time) error
}

type batchFailureRecorder interface {
	FailBatch(context.Context, application.BatchWork, domain.FailureCategory, time.Time) (bool, error)
}

type batchProcessor interface {
	Process(context.Context, application.BatchWork) error
}

type vectorCleanupRepository interface {
	PendingVectorCleanup(context.Context, int, time.Time) ([]repository.VectorCleanupJob, error)
	CompleteVectorCleanup(context.Context, string) error
	RetryVectorCleanup(context.Context, string, time.Time) error
}

type vectorRuntime interface {
	EnsureCollection(context.Context) error
	CheckReady(context.Context) error
	DeactivateJob(context.Context, string) error
	DeleteJob(context.Context, string) error
}

type lifecycleProcessor interface {
	HandleReindex(context.Context, application.LifecycleEvent) error
	HandleDeletion(context.Context, application.LifecycleEvent) error
	RetryDeletions(context.Context, int) error
}

type Runtime struct {
	configuration config.WorkerConfig
	pool          *pgxpool.Pool
	repository    *repository.Postgres
	manifestFails manifestFailureRecorder
	batchFails    batchFailureRecorder
	vectorJobs    vectorCleanupRepository
	objects       storage.ObjectStore
	planner       *application.Planner
	indexer       batchProcessor
	lifecycle     lifecycleProcessor
	embedder      *embedding.TEI
	vector        vectorRuntime
	diagnostic    *diagnostic.Recorder
	log           *zap.Logger
}

type retryPublisher interface {
	Publish(context.Context, string, string, amqp091.Publishing) error
}

func New(ctx context.Context, configuration config.WorkerConfig, recorder *diagnostic.Recorder, log *zap.Logger) (*Runtime, error) {
	pool, err := pgxpool.New(ctx, configuration.DSN)
	if err != nil {
		return nil, errors.New("configure retrieval database")
	}
	probeContext, cancel := context.WithTimeout(ctx, configuration.DBPingTimeout)
	defer cancel()
	if err = pool.Ping(probeContext); err != nil {
		pool.Close()
		return nil, errors.New("retrieval database unavailable")
	}
	objects, err := storage.NewMinIO(configuration.MinIOEndpoint, configuration.MinIOAccessKey, configuration.MinIOSecretKey, configuration.ArtifactBucket, !configuration.MinIOInsecure)
	if err != nil {
		pool.Close()
		return nil, err
	}
	records := repository.NewPostgres(pool, repository.Policy{FinalizationLease: configuration.FinalizationLease})
	manifestPolicy := application.ManifestPolicy{
		MaxPages:              uint32(configuration.ManifestMaxPages),
		MaxShards:             configuration.ManifestMaxShards,
		MaxShardCompressed:    configuration.ManifestMaxShardCompressedBytes,
		MaxShardExpanded:      configuration.ManifestMaxShardExpandedBytes,
		MaxShardChunks:        uint32(configuration.ManifestMaxShardChunks),
		MaxTotalChunks:        uint32(configuration.ManifestMaxTotalChunks),
		MaxExpandedTotalBytes: configuration.ManifestMaxExpandedBytes,
	}
	planner, err := application.NewPlanner(records, randomID, time.Now, manifestPolicy)
	if err != nil {
		pool.Close()
		return nil, err
	}
	httpClient := retrievalruntime.NewDependencyHTTPClient(configuration.DependencyTimeout)
	embedder, err := retrievalruntime.NewWorkerEmbedder(configuration, httpClient, log)
	if err != nil {
		pool.Close()
		return nil, err
	}
	index, err := retrievalruntime.NewWorkerVectorStore(configuration, httpClient)
	if err != nil {
		pool.Close()
		return nil, err
	}
	reader, err := artifact.NewReader(objects)
	if err != nil {
		pool.Close()
		return nil, err
	}
	indexer, err := application.NewIndexer(records, reader, embedder, index, time.Now, manifestPolicy)
	if err != nil {
		pool.Close()
		return nil, err
	}
	lifecycle, err := application.NewLifecycleCoordinator(records, index, randomID, time.Now)
	if err != nil {
		pool.Close()
		return nil, err
	}
	return &Runtime{configuration: configuration, pool: pool, repository: records, manifestFails: records, batchFails: records, vectorJobs: records, objects: objects, planner: planner, indexer: indexer, lifecycle: lifecycle, embedder: embedder, vector: index, diagnostic: recorder, log: log}, nil
}

// Close releases resources owned by a one-message runtime.
func (r *Runtime) Close() {
	if r != nil && r.pool != nil {
		r.pool.Close()
	}
}

func (r *Runtime) Run(ctx context.Context) error {
	collectionContext, collectionCancel := context.WithTimeout(ctx, r.configuration.CollectionEnsureTimeout)
	collectionErr := r.vector.EnsureCollection(collectionContext)
	collectionCancel()
	if collectionErr != nil {
		return errors.New("initialize vector collection")
	}
	if err := awaitReadiness(ctx, r.embedder.CheckReady, r.configuration.ReadinessMaxAttempts, r.configuration.ReadinessInitialDelay, r.configuration.ReadinessMaxDelay, r.configuration.ReadinessProbeTimeout); err != nil {
		return errors.New("wait for embedding service readiness")
	}
	go r.serveReadiness(ctx)
	return r.runBrokerLoop(ctx, r.runBrokerSession, r.configuration.ReconnectInitialBackoff, r.configuration.ReconnectMaxBackoff)
}

func awaitReadiness(ctx context.Context, check func(context.Context) error, maxAttempts int, initialDelay, maxDelay, probeTimeout time.Duration) error {
	if check == nil || maxAttempts <= 0 {
		return errors.New("invalid readiness probe")
	}
	delay := initialDelay
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		probeContext, cancel := context.WithTimeout(ctx, probeTimeout)
		err := check(probeContext)
		cancel()
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if attempt == maxAttempts {
			return err
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		delay *= 2
		if delay > maxDelay {
			delay = maxDelay
		}
	}
	return nil
}

func (r *Runtime) runBrokerLoop(ctx context.Context, run func(context.Context) error, initialBackoff, maximumBackoff time.Duration) error {
	backoff := initialBackoff
	for ctx.Err() == nil {
		err := run(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			r.logBrokerSessionReconnecting()
		}
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
		if backoff < maximumBackoff {
			backoff *= 2
			if backoff > maximumBackoff {
				backoff = maximumBackoff
			}
		}
	}
	return nil
}

func (r *Runtime) runBrokerSession(ctx context.Context) error {
	sessionContext, sessionCancel := context.WithCancel(ctx)
	defer sessionCancel()
	consumerConnection, err := rabbitmq.Dial(ctx, r.configuration.ConsumerRabbitURI, rabbitmq.DialPolicy{
		Timeout:   r.configuration.RabbitDialTimeout,
		Heartbeat: r.configuration.RabbitHeartbeat,
	})
	if err != nil {
		return errors.New("retrieval consumer broker unavailable")
	}
	defer func() { _ = consumerConnection.Close() }()
	publisherConnection, err := rabbitmq.Dial(ctx, r.configuration.PublisherRabbitURI, rabbitmq.DialPolicy{
		Timeout:   r.configuration.RabbitDialTimeout,
		Heartbeat: r.configuration.RabbitHeartbeat,
	})
	if err != nil {
		return errors.New("retrieval publisher broker unavailable")
	}
	defer func() { _ = publisherConnection.Close() }()
	consumerChannel, err := consumerConnection.Channel()
	if err != nil {
		return errors.New("open retrieval consumer channel")
	}
	defer func() { _ = consumerChannel.Close() }()
	if err = consumerChannel.Qos(r.configuration.Concurrency, 0, false); err != nil {
		return errors.New("configure retrieval prefetch")
	}
	publisherChannel, err := publisherConnection.Channel()
	if err != nil {
		return errors.New("open retrieval publisher channel")
	}
	defer func() { _ = publisherChannel.Close() }()
	if err = publisherChannel.Confirm(false); err != nil {
		return errors.New("enable retrieval publisher confirms")
	}
	publisher := rabbitmq.NewPublisher(publisherChannel)
	metadataDeliveries, err := consumerChannel.Consume(metadataQueue, "", false, false, false, false, nil)
	if err != nil {
		return errors.New("consume metadata queue")
	}
	manifestDeliveries, err := consumerChannel.Consume(manifestQueue, "", false, false, false, false, nil)
	if err != nil {
		return errors.New("consume manifest queue")
	}
	batchDeliveries, err := consumerChannel.Consume(batchQueue, "", false, false, false, false, nil)
	if err != nil {
		return errors.New("consume batch queue")
	}
	lifecycleDeliveries, err := consumerChannel.Consume(lifecycleQueue, "", false, false, false, false, nil)
	if err != nil {
		return errors.New("consume lifecycle queue")
	}
	consumerConnectionClosed := consumerConnection.NotifyClose(make(chan *amqp091.Error, 1))
	publisherConnectionClosed := publisherConnection.NotifyClose(make(chan *amqp091.Error, 1))
	consumerChannelClosed := consumerChannel.NotifyClose(make(chan *amqp091.Error, 1))
	publisherChannelClosed := publisherChannel.NotifyClose(make(chan *amqp091.Error, 1))
	semaphore := make(chan struct{}, r.configuration.Concurrency)
	var handlers sync.WaitGroup
	defer handlers.Wait()
	dispatchTicker := time.NewTicker(r.configuration.DispatchInterval)
	defer dispatchTicker.Stop()
	cleanupTicker := time.NewTicker(r.configuration.CleanupInterval)
	defer cleanupTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-consumerConnectionClosed:
			sessionCancel()
			return errors.New("retrieval consumer connection closed")
		case <-publisherConnectionClosed:
			sessionCancel()
			return errors.New("retrieval publisher connection closed")
		case <-consumerChannelClosed:
			sessionCancel()
			return errors.New("retrieval consumer channel closed")
		case <-publisherChannelClosed:
			sessionCancel()
			return errors.New("retrieval publisher channel closed")
		case delivery, open := <-metadataDeliveries:
			if !open {
				sessionCancel()
				return errors.New("metadata delivery channel closed")
			}
			r.handle(sessionContext, semaphore, &handlers, publisher, metadataQueue, delivery, r.handleMetadata, nil)
		case delivery, open := <-manifestDeliveries:
			if !open {
				sessionCancel()
				return errors.New("manifest delivery channel closed")
			}
			r.handle(sessionContext, semaphore, &handlers, publisher, manifestQueue, delivery, r.handleManifest, r.failManifestArtifactRead)
		case delivery, open := <-batchDeliveries:
			if !open {
				sessionCancel()
				return errors.New("batch delivery channel closed")
			}
			r.handle(sessionContext, semaphore, &handlers, publisher, batchQueue, delivery, r.handleBatch, r.failBatch)
		case delivery, open := <-lifecycleDeliveries:
			if !open {
				sessionCancel()
				return errors.New("lifecycle delivery channel closed")
			}
			r.handle(sessionContext, semaphore, &handlers, publisher, lifecycleQueue, delivery, func(handlerContext context.Context, payload []byte) error {
				return r.handleLifecycle(handlerContext, delivery.Type, payload)
			}, nil)
		case <-dispatchTicker.C:
			r.dispatchOutbox(sessionContext, publisher)
		case now := <-cleanupTicker.C:
			cleanupContext, cleanupCancel := context.WithTimeout(sessionContext, r.configuration.CleanupTimeout)
			recovered, _ := r.repository.RecoverStaleBatches(cleanupContext, now.UTC().Add(-r.configuration.StaleBatchAge), now.UTC())
			r.logStaleBatchesRecovered(recovered)
			_ = r.retryPendingVectorCleanup(cleanupContext, now.UTC(), r.configuration.CleanupBatchSize)
			if r.lifecycle != nil {
				_ = r.lifecycle.RetryDeletions(cleanupContext, r.configuration.CleanupBatchSize)
			}
			cleanupCancel()
		}
	}
}

func (r *Runtime) serveReadiness(ctx context.Context) {
	mux := http.NewServeMux()
	mux.HandleFunc("/readyz", func(writer http.ResponseWriter, request *http.Request) {
		probeContext, cancel := context.WithTimeout(request.Context(), r.configuration.ReadinessProbeTimeout)
		defer cancel()
		if r.pool.Ping(probeContext) != nil || r.embedder.CheckReady(probeContext) != nil || r.vector.CheckReady(probeContext) != nil {
			http.Error(writer, "not ready", http.StatusServiceUnavailable)
			return
		}
		writer.WriteHeader(http.StatusNoContent)
	})
	server := &http.Server{Addr: r.configuration.MetricsAddress, Handler: mux, ReadHeaderTimeout: r.configuration.ReadinessReadHeaderTimeout, IdleTimeout: r.configuration.ReadinessIdleTimeout}
	go func() {
		<-ctx.Done()
		shutdownContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), r.configuration.ReadinessShutdownTimeout)
		defer cancel()
		_ = server.Shutdown(shutdownContext)
	}()
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Printf("retrieval worker readiness listener stopped: %v", err)
	}
}

func (r *Runtime) handle(ctx context.Context, semaphore chan struct{}, handlers *sync.WaitGroup, publisher retryPublisher, sourceQueue string, delivery amqp091.Delivery, handler func(context.Context, []byte) error, terminalFailure func(context.Context, []byte, error) error) {
	semaphore <- struct{}{}
	handlers.Add(1)
	go func() {
		defer handlers.Done()
		defer func() { <-semaphore }()
		maxRetryAttempts := r.maximumRetryAttempts()
		currentRetryAttempt := retryAttempt(delivery.Headers, maxRetryAttempts)
		bookID := deliveryBookID(sourceQueue, delivery.Type, delivery.Body)
		r.logDeliveryReceived(sourceQueue, delivery.Type, delivery.MessageId, bookID, currentRetryAttempt)
		if delivery.ContentType != "application/x-protobuf" || len(delivery.Body) == 0 || len(delivery.Body) > contracts.MaximumBrokerMessageBytes {
			r.logRejectedDelivery(sourceQueue, delivery.Type, delivery.ContentType, "invalid_delivery", sanitizeFailureDetail("body constraints"))
			settleNack(ctx, delivery, false)
			r.logDeliverySettled(sourceQueue, delivery.Type, delivery.MessageId, bookID, "nack")
			return
		}
		handleContext, cancel := context.WithTimeout(ctx, r.configuration.ServerlessInvocationTimeout)
		err := handler(handleContext, delivery.Body)
		cancel()
		if err == nil {
			settleAck(ctx, delivery)
			r.logDeliverySettled(sourceQueue, delivery.Type, delivery.MessageId, bookID, "ack")
			return
		}
		if terminalFailure != nil && application.TerminalIndexingFailure(err) {
			failureContext, failureCancel := context.WithTimeout(ctx, r.configuration.FailureRecordTimeout)
			failureErr := terminalFailure(failureContext, delivery.Body, err)
			failureCancel()
			if failureErr == nil {
				settleAck(ctx, delivery)
				r.logDeliverySettled(sourceQueue, delivery.Type, delivery.MessageId, bookID, "ack")
				return
			}
			r.logRetry(sourceQueue, "terminal_failure_record_failed", sanitizeFailureDetail(failureErr.Error()))
			nextAttempt, retry := failureRecordingRetryAttempt(delivery.Headers, maxRetryAttempts)
			if !retry {
				settleNack(ctx, delivery, false)
				r.logDeliverySettled(sourceQueue, delivery.Type, delivery.MessageId, bookID, "nack")
				return
			}
			if r.publishRetry(ctx, publisher, sourceQueue, delivery, nextAttempt) == nil {
				r.logRetryPublished(sourceQueue, delivery.Type, delivery.MessageId, bookID, nextAttempt)
				settleAck(ctx, delivery)
				r.logDeliverySettled(sourceQueue, delivery.Type, delivery.MessageId, bookID, "ack")
				return
			}
			r.logRetryPublishFailed(sourceQueue, "retry_publish_failed")
			if ctx.Err() == nil {
				settleNack(ctx, delivery, false)
				r.logDeliverySettled(sourceQueue, delivery.Type, delivery.MessageId, bookID, "nack")
			}
			return
		}
		if errors.Is(err, application.ErrInvalidEvent) || errors.Is(err, application.ErrConflictingEvent) {
			r.logRejectedDelivery(sourceQueue, delivery.Type, delivery.ContentType, rejectionReason(err), sanitizeFailureDetail(err.Error()))
			settleNack(ctx, delivery, false)
			r.logDeliverySettled(sourceQueue, delivery.Type, delivery.MessageId, bookID, "nack")
			return
		}
		if currentRetryAttempt >= maxRetryAttempts {
			if terminalFailure == nil {
				r.logRejectedDelivery(sourceQueue, delivery.Type, delivery.ContentType, "invalid_event", sanitizeFailureDetail(err.Error()))
				settleNack(ctx, delivery, false)
				r.logDeliverySettled(sourceQueue, delivery.Type, delivery.MessageId, bookID, "nack")
				return
			}
			failureContext, failureCancel := context.WithTimeout(ctx, r.configuration.FailureRecordTimeout)
			failureErr := terminalFailure(failureContext, delivery.Body, err)
			failureCancel()
			if failureErr == nil {
				settleAck(ctx, delivery)
				r.logDeliverySettled(sourceQueue, delivery.Type, delivery.MessageId, bookID, "ack")
				return
			}
			r.logRejectedDelivery(sourceQueue, delivery.Type, delivery.ContentType, "terminal_failure_record_failed", sanitizeFailureDetail(failureErr.Error()))
			settleNack(ctx, delivery, false)
			r.logDeliverySettled(sourceQueue, delivery.Type, delivery.MessageId, bookID, "nack")
			return
		}
		r.logRetry(sourceQueue, rejectionReason(err), sanitizeFailureDetail(err.Error()))
		nextAttempt := currentRetryAttempt + 1
		if r.publishRetry(ctx, publisher, sourceQueue, delivery, nextAttempt) == nil {
			r.logRetryPublished(sourceQueue, delivery.Type, delivery.MessageId, bookID, nextAttempt)
			settleAck(ctx, delivery)
			r.logDeliverySettled(sourceQueue, delivery.Type, delivery.MessageId, bookID, "ack")
			return
		}
		r.logRetryPublishFailed(sourceQueue, "retry_publish_failed")
		if ctx.Err() == nil {
			settleNack(ctx, delivery, false)
			r.logDeliverySettled(sourceQueue, delivery.Type, delivery.MessageId, bookID, "nack")
		}
	}()
}

func settleAck(ctx context.Context, delivery amqp091.Delivery) {
	if ctx.Err() == nil {
		_ = delivery.Ack(false)
	}
}
func settleNack(ctx context.Context, delivery amqp091.Delivery, requeue bool) {
	if ctx.Err() == nil {
		_ = delivery.Nack(false, requeue)
	}
}

func (r *Runtime) publishRetry(ctx context.Context, publisher retryPublisher, sourceQueue string, delivery amqp091.Delivery, attempt int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	routingKey, err := retryRoutingKey(sourceQueue, attempt, r.maximumRetryAttempts())
	if err != nil {
		return err
	}
	publishContext, cancel := context.WithTimeout(ctx, r.configuration.PublishTimeout)
	defer cancel()
	return publisher.Publish(publishContext, retryExchange, routingKey, amqp091.Publishing{
		// Only the application-owned attempt counter crosses the retry boundary.
		// Broker death/routing headers and publisher-controlled identity/reply
		// properties are intentionally excluded.
		Headers: amqp091.Table{"x-retry-attempt": attempt}, ContentType: delivery.ContentType, ContentEncoding: delivery.ContentEncoding,
		DeliveryMode: amqp091.Persistent, Priority: delivery.Priority, CorrelationId: delivery.CorrelationId,
		MessageId: delivery.MessageId, Timestamp: delivery.Timestamp, Type: delivery.Type, AppId: delivery.AppId,
		Body: append([]byte(nil), delivery.Body...),
	})
}

func failureRecordingRetryAttempt(headers amqp091.Table, maxRetryAttempts int64) (int64, bool) {
	attempt := retryAttempt(headers, maxRetryAttempts)
	if attempt >= maxRetryAttempts {
		return 0, false
	}
	return attempt + 1, true
}

func retryRoutingKey(sourceQueue string, attempt, maxRetryAttempts int64) (string, error) {
	if attempt < 1 || attempt > maxRetryAttempts {
		return "", errors.New("invalid retry attempt")
	}
	delay := "30s"
	if attempt == 1 {
		delay = "5s"
	}
	switch sourceQueue {
	case metadataQueue, manifestQueue, batchQueue, lifecycleQueue:
		return sourceQueue + ".retry." + delay, nil
	default:
		return "", errors.New("unknown retry source queue")
	}
}

func (r *Runtime) handleMetadata(ctx context.Context, payload []byte) error {
	event, err := transport.DecodeMetadata(payload)
	if err != nil {
		return err
	}
	r.logMetadataReceived(event.BookID)
	if err = r.planner.HandleMetadata(ctx, event); err != nil {
		return err
	}
	r.logMetadataCompleted(event.BookID)
	return nil
}

// ProcessOne runs one already-authenticated delivery through the same typed
// handlers used by the long-running AMQP worker. Broker settlement remains the
// responsibility of the caller.
func (r *Runtime) ProcessOne(ctx context.Context, queue, eventType string, payload []byte) error {
	switch queue {
	case metadataQueue:
		if eventType != contracts.EventCatalogBookUploaded {
			return application.ErrInvalidEvent
		}
		return r.handleMetadata(ctx, payload)
	case manifestQueue:
		if eventType != contracts.EventIngestionBookChunksReady {
			return application.ErrInvalidEvent
		}
		return r.handleManifest(ctx, payload)
	case batchQueue:
		if eventType != contracts.EventRetrievalIndexBatch {
			return application.ErrInvalidEvent
		}
		return r.handleBatch(ctx, payload)
	case lifecycleQueue:
		return r.handleLifecycle(ctx, eventType, payload)
	default:
		return application.ErrInvalidEvent
	}
}

// ProcessOneDelivery applies the long-running worker's bounded retry republish
// and DLQ policy to one AMQP delivery. It waits for settlement before returning.
func (r *Runtime) ProcessOneDelivery(ctx context.Context, publisher retryPublisher, queue string, delivery amqp091.Delivery) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var handler func(context.Context, []byte) error
	var terminalFailure func(context.Context, []byte, error) error
	switch queue {
	case metadataQueue:
		handler = r.handleMetadata
	case manifestQueue:
		handler = r.handleManifest
		terminalFailure = r.failManifestArtifactRead
	case batchQueue:
		handler = r.handleBatch
		terminalFailure = r.failBatch
	case lifecycleQueue:
		handler = func(handlerCtx context.Context, payload []byte) error {
			return r.handleLifecycle(handlerCtx, delivery.Type, payload)
		}
	default:
		return application.ErrInvalidEvent
	}
	semaphore := make(chan struct{}, 1)
	var handlers sync.WaitGroup
	r.handle(ctx, semaphore, &handlers, publisher, queue, delivery, handler, terminalFailure)
	handlers.Wait()
	return ctx.Err()
}

func (r *Runtime) handleManifest(ctx context.Context, payload []byte) error {
	event, err := transport.DecodeManifestEnvelope(payload)
	if err != nil {
		return err
	}
	r.logManifestReceived(event.BookID)
	manifestPayload, err := r.objects.ReadBounded(ctx, event.ManifestReference, contracts.MaximumManifestBytes)
	if err != nil {
		return errors.Join(errManifestArtifactRead, err)
	}
	event, err = transport.DecodeManifest(payload, manifestPayload)
	if err != nil {
		if category, terminal := application.ManifestFailureCategory(event, err); terminal {
			recordErr := r.manifestFailureRecorder().FailManifest(ctx, event, category, time.Now().UTC())
			if recordErr == nil {
				r.logManifestTerminalFailureRecorded(event.BookID, reasonFromCategory(category))
			}
			return recordErr
		}
		return err
	}
	if err = r.planner.HandleManifest(ctx, event); err != nil {
		return err
	}
	r.logManifestCompleted(event.BookID)
	return nil
}

func (r *Runtime) handleBatch(ctx context.Context, payload []byte) error {
	work, err := transport.DecodeBatch(payload)
	if err != nil {
		return err
	}
	r.logBatchReceived(work.BookID)
	if err = r.indexer.Process(ctx, work); err != nil {
		r.logBatchFailed(work.BookID, rejectionReason(err), sanitizeFailureDetail(err.Error()))
		return err
	}
	r.logBatchCompleted(work.BookID)
	return nil
}

func (r *Runtime) handleLifecycle(ctx context.Context, eventType string, payload []byte) error {
	if r.lifecycle == nil {
		return errors.New("lifecycle processor unavailable")
	}
	switch eventType {
	case "catalog.book.reindex-requested.v1":
		event, err := transport.DecodeReindex(payload)
		if err != nil {
			return err
		}
		return r.lifecycle.HandleReindex(ctx, event)
	case "catalog.book.deletion-requested.v1":
		event, err := transport.DecodeDeletion(payload)
		if err != nil {
			return err
		}
		return r.lifecycle.HandleDeletion(ctx, event)
	default:
		return application.ErrInvalidEvent
	}
}

func (r *Runtime) failManifestArtifactRead(ctx context.Context, payload []byte, err error) error {
	if !errors.Is(err, errManifestArtifactRead) {
		return err
	}
	event, decodeErr := transport.DecodeManifestEnvelope(payload)
	if decodeErr != nil {
		return decodeErr
	}
	recordErr := r.manifestFailureRecorder().FailManifest(ctx, event, domain.FailureManifestIntegrity, time.Now().UTC())
	if recordErr == nil {
		r.logManifestTerminalFailureRecorded(event.BookID, "manifest_artifact_read_failed")
	}
	return recordErr
}

func (r *Runtime) failBatch(ctx context.Context, payload []byte, failure error) error {
	work, err := transport.DecodeBatch(payload)
	if err != nil {
		return err
	}
	category := application.FailureCategory(failure)
	transitioned, err := r.batchFailureRecorder().FailBatch(ctx, work, category, time.Now().UTC())
	if err == nil && transitioned {
		r.logBatchTerminalFailureRecorded(work.BookID, reasonFromCategory(category))
	}
	if err != nil {
		return err
	}
	if !transitioned {
		return nil
	}
	if r.vector == nil {
		return nil
	}
	if err = r.vector.DeleteJob(ctx, work.JobID); err != nil {
		r.logVectorDeactivateFailed(work.BookID)
		return nil
	}
	return r.vectorCleanupRepository().CompleteVectorCleanup(ctx, work.JobID)
}

func (r *Runtime) manifestFailureRecorder() manifestFailureRecorder {
	if r.manifestFails != nil {
		return r.manifestFails
	}
	return r.repository
}

func (r *Runtime) batchFailureRecorder() batchFailureRecorder {
	if r.batchFails != nil {
		return r.batchFails
	}
	return r.repository
}

func (r *Runtime) vectorCleanupRepository() vectorCleanupRepository {
	if r.vectorJobs != nil {
		return r.vectorJobs
	}
	return r.repository
}

func (r *Runtime) retryPendingVectorCleanup(ctx context.Context, now time.Time, limit int) error {
	if r.vector == nil {
		return nil
	}
	jobs, err := r.vectorCleanupRepository().PendingVectorCleanup(ctx, limit, now)
	if err != nil {
		return err
	}
	for _, job := range jobs {
		if err = r.vector.DeleteJob(ctx, job.JobID); err != nil {
			r.logVectorDeactivateFailed(job.BookID)
			if retryErr := r.vectorCleanupRepository().RetryVectorCleanup(ctx, job.JobID, now); retryErr != nil {
				return retryErr
			}
			continue
		}
		if err = r.vectorCleanupRepository().CompleteVectorCleanup(ctx, job.JobID); err != nil {
			return err
		}
	}
	return nil
}

func deliveryAttempt(headers amqp091.Table) int64 {
	value, found := headers["x-delivery-count"]
	if !found {
		return 0
	}
	switch count := value.(type) {
	case int64:
		return count
	case int32:
		return int64(count)
	default:
		return 5
	}
}

func retryAttempt(headers amqp091.Table, maxRetryAttempts int64) int64 {
	value, found := headers["x-retry-attempt"]
	if !found {
		return deliveryAttempt(headers)
	}
	switch count := value.(type) {
	case int64:
		if count >= 0 && count <= maxRetryAttempts {
			return count
		}
	case int32:
		if count >= 0 && int64(count) <= maxRetryAttempts {
			return int64(count)
		}
	}
	return maxRetryAttempts
}

func (r *Runtime) maximumRetryAttempts() int64 {
	if r.configuration.MaxRetryAttempts > 0 {
		return int64(r.configuration.MaxRetryAttempts)
	}
	return 4
}

func deliveryBookID(queue, eventType string, payload []byte) string {
	switch queue {
	case metadataQueue:
		event, err := transport.DecodeMetadata(payload)
		if err == nil {
			return event.BookID
		}
	case manifestQueue:
		event, err := transport.DecodeManifestEnvelope(payload)
		if err == nil {
			return event.BookID
		}
	case batchQueue:
		work, err := transport.DecodeBatch(payload)
		if err == nil {
			return work.BookID
		}
	case lifecycleQueue:
		if eventType == "catalog.book.reindex-requested.v1" {
			event, err := transport.DecodeReindex(payload)
			if err == nil {
				return event.BookID
			}
		}
		if eventType == "catalog.book.deletion-requested.v1" {
			event, err := transport.DecodeDeletion(payload)
			if err == nil {
				return event.BookID
			}
		}
	}
	return ""
}

func (r *Runtime) dispatchOutbox(ctx context.Context, publisher *rabbitmq.Publisher) {
	records, err := r.repository.PendingOutbox(ctx, 20, time.Now().UTC())
	if err != nil {
		return
	}
	for _, record := range records {
		publishContext, cancel := context.WithTimeout(ctx, r.configuration.PublishTimeout)
		publishErr := publisher.Publish(publishContext, eventExchange, record.EventType, amqp091.Publishing{
			ContentType: "application/x-protobuf", DeliveryMode: amqp091.Persistent, Type: record.EventType, MessageId: record.EventID, Timestamp: time.Now().UTC(), Body: record.Payload,
		})
		cancel()
		if publishErr != nil {
			_ = r.repository.DeferOutbox(ctx, record.EventID, time.Now().UTC())
			r.logOutboxDeferred("outbox_publish_failed")
			continue
		}
		r.logOutboxPublished()
		if err = r.repository.MarkPublished(ctx, record.EventID, time.Now().UTC()); err != nil {
			r.logOutboxDeferred("outbox_mark_failed")
			continue
		}
		r.logOutboxMarkedPublished()
	}
}

func rejectionReason(err error) string {
	category := application.FailureCategory(err)
	switch {
	case errors.Is(err, application.ErrConflictingEvent):
		return "conflicting_event"
	case errors.Is(err, application.ErrInvalidEvent):
		return "invalid_event"
	case category != domain.FailureInternalIndexing:
		return reasonFromCategory(category)
	default:
		return "unknown_failure"
	}
}

func (r *Runtime) logRejectedDelivery(queue, eventType, contentType, reason, detail string) {
	if r.diagnostic == nil {
		return
	}
	switch queue {
	case metadataQueue:
		r.diagnostic.Rejected(queueOperation(queue), eventType, contentType, reason, detail)
	case manifestQueue:
		r.diagnostic.Rejected(queueOperation(queue), eventType, contentType, reason, detail)
	case batchQueue:
		r.diagnostic.Rejected(queueOperation(queue), eventType, contentType, reason, detail)
	case lifecycleQueue:
		r.diagnostic.Rejected(queueOperation(queue), eventType, contentType, reason, detail)
	}
}

func sanitizeFailureDetail(value string) string {
	value = strings.TrimSpace(strings.Join(strings.Fields(value), " "))
	if value == "" {
		return ""
	}
	if len([]rune(value)) > 160 {
		value = string([]rune(value)[:160])
	}
	return value
}

func reasonFromCategory(category domain.FailureCategory) string {
	switch category {
	case domain.FailureManifestIntegrity:
		return "manifest_integrity"
	case domain.FailureIncompatibleProfile:
		return "incompatible_profile"
	case domain.FailureEmbeddingUnavailable:
		return "embedding_unavailable"
	case domain.FailureVectorStoreUnavailable:
		return "vector_store_unavailable"
	case domain.FailureResourceLimit:
		return "resource_limit_exceeded"
	case domain.FailureIndexingTimeout:
		return "indexing_timeout"
	case domain.FailureInternalIndexing:
		return "internal_indexing_error"
	default:
		return "unknown_failure"
	}
}

func queueOperation(queue string) string {
	switch queue {
	case metadataQueue:
		return "metadata_queue"
	case manifestQueue:
		return "manifest_queue"
	case batchQueue:
		return "batch_queue"
	case lifecycleQueue:
		return "lifecycle_queue"
	default:
		return "batch_queue"
	}
}

func (r *Runtime) logBrokerSessionReconnecting() {
	if r.diagnostic != nil {
		r.diagnostic.BrokerSessionReconnecting()
		return
	}
	log.Print("retrieval worker broker session stopped; reconnecting")
}

func (r *Runtime) logStaleBatchesRecovered(count int64) {
	if r.diagnostic != nil {
		r.diagnostic.StaleBatchesRecovered(count)
	}
}

func (r *Runtime) logRetry(queue, reason, detail string) {
	if r.diagnostic != nil {
		r.diagnostic.RetryScheduled(queueOperation(queue), reason, detail)
	}
}

func (r *Runtime) logRetryPublishFailed(queue, reason string) {
	if r.diagnostic != nil {
		r.diagnostic.RetryPublishFailed(queueOperation(queue), reason)
	}
}

func (r *Runtime) logDeliveryReceived(queue, eventType, messageID, bookID string, attempt int64) {
	if r.diagnostic != nil {
		r.diagnostic.DeliveryReceived(queueOperation(queue), eventType, messageID, bookID, attempt)
	}
}

func (r *Runtime) logDeliverySettled(queue, eventType, messageID, bookID, disposition string) {
	if r.diagnostic != nil {
		r.diagnostic.DeliverySettled(queueOperation(queue), eventType, messageID, bookID, disposition)
	}
}

func (r *Runtime) logRetryPublished(queue, eventType, messageID, bookID string, attempt int64) {
	if r.diagnostic != nil {
		r.diagnostic.RetryPublished(queueOperation(queue), eventType, messageID, bookID, attempt)
	}
}

func (r *Runtime) logMetadataReceived(bookID string) {
	if r.diagnostic != nil {
		r.diagnostic.MetadataReceived(bookID)
	}
}

func (r *Runtime) logMetadataCompleted(bookID string) {
	if r.diagnostic != nil {
		r.diagnostic.MetadataCompleted(bookID)
	}
}

func (r *Runtime) logManifestReceived(bookID string) {
	if r.diagnostic != nil {
		r.diagnostic.ManifestReceived(bookID)
	}
}

func (r *Runtime) logManifestCompleted(bookID string) {
	if r.diagnostic != nil {
		r.diagnostic.ManifestCompleted(bookID)
	}
}

func (r *Runtime) logManifestTerminalFailureRecorded(bookID, reason string) {
	if r.diagnostic != nil {
		r.diagnostic.ManifestTerminalFailureRecorded(bookID, reason)
	}
}

func (r *Runtime) logBatchReceived(bookID string) {
	if r.diagnostic != nil {
		r.diagnostic.BatchReceived(bookID)
	}
}

func (r *Runtime) logBatchCompleted(bookID string) {
	if r.diagnostic != nil {
		r.diagnostic.BatchCompleted(bookID)
	}
}

func (r *Runtime) logBatchFailed(bookID, reason, detail string) {
	if r.diagnostic != nil {
		r.diagnostic.BatchFailed(bookID, reason, detail)
	}
}

func (r *Runtime) logBatchTerminalFailureRecorded(bookID, reason string) {
	if r.diagnostic != nil {
		r.diagnostic.BatchTerminalFailureRecorded(bookID, reason)
	}
}

func (r *Runtime) logVectorDeactivateFailed(bookID string) {
	if r.diagnostic != nil {
		r.diagnostic.VectorDeactivateFailed(bookID)
		return
	}
	log.Print("retrieval worker failed to deactivate terminal batch vectors")
}

func (r *Runtime) logOutboxPublished() {
	if r.diagnostic != nil {
		r.diagnostic.OutboxPublished()
	}
}

func (r *Runtime) logOutboxDeferred(reason string) {
	if r.diagnostic != nil {
		r.diagnostic.OutboxDeferred(reason)
	}
}

func (r *Runtime) logOutboxMarkedPublished() {
	if r.diagnostic != nil {
		r.diagnostic.OutboxMarkedPublished()
	}
}

func randomID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func LogFailure() {
	log.Print("retrieval worker stopped because a required dependency was unavailable")
}

func LogFailureWithError(err error) {
	if err == nil {
		LogFailure()
		return
	}
	log.Printf("retrieval worker stopped: reason=runtime_failure error=%v", err)
}
