// Package worker composes the portable local data-preparation adapter.
package worker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/belLena81/raglibrarian/pkg/process"
	"github.com/belLena81/raglibrarian/services/retrieval-service/config"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/application"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/artifact"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/domain"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/embedding"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/rabbitmq"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/repository"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/storage"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/transport"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/vector"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rabbitmq/amqp091-go"
)

const (
	metadataQueue         = "retrieval.book-uploaded.v1"
	manifestQueue         = "retrieval.chunks-ready.v1"
	batchQueue            = "retrieval.index-batch.v1"
	eventExchange         = "raglibrarian.retrieval.events.v1"
	retryExchange         = "raglibrarian.retrieval.retry.v1"
	initialReconnectDelay = time.Second
	maxReconnectDelay     = 30 * time.Second
	maximumRetryAttempts  = int64(4)
)

var errManifestArtifactRead = errors.New("manifest artifact read failed")

type manifestFailureRecorder interface {
	FailManifest(context.Context, application.ManifestEvent, domain.FailureCategory, time.Time) error
}

type Runtime struct {
	configuration config.WorkerConfig
	pool          *pgxpool.Pool
	repository    *repository.Postgres
	manifestFails manifestFailureRecorder
	objects       storage.ObjectStore
	planner       *application.Planner
	indexer       *application.Indexer
	embedder      *embedding.TEI
	vector        *vector.Qdrant
}

type retryPublisher interface {
	Publish(context.Context, string, string, amqp091.Publishing) error
}

func New(ctx context.Context, configuration config.WorkerConfig) (*Runtime, error) {
	pool, err := pgxpool.New(ctx, configuration.DSN)
	if err != nil {
		return nil, errors.New("configure retrieval database")
	}
	probeContext, cancel := context.WithTimeout(ctx, 5*time.Second)
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
	records := repository.NewPostgres(pool)
	planner, err := application.NewPlanner(records, randomID, time.Now)
	if err != nil {
		pool.Close()
		return nil, err
	}
	httpClient := &http.Client{Timeout: 90 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	embedder, err := embedding.NewTEI(configuration.TEIURL, httpClient)
	if err != nil {
		pool.Close()
		return nil, err
	}
	index, err := vector.NewAuthenticatedQdrant(configuration.QdrantURL, configuration.QdrantCollection, configuration.QdrantAPIKey, httpClient)
	if err != nil {
		pool.Close()
		return nil, err
	}
	reader, err := artifact.NewReader(objects)
	if err != nil {
		pool.Close()
		return nil, err
	}
	indexer, err := application.NewIndexer(records, reader, embedder, index, time.Now)
	if err != nil {
		pool.Close()
		return nil, err
	}
	return &Runtime{configuration: configuration, pool: pool, repository: records, manifestFails: records, objects: objects, planner: planner, indexer: indexer, embedder: embedder, vector: index}, nil
}

func (r *Runtime) Run(ctx context.Context) error {
	if err := process.DropPrivileges(r.configuration.RunAs); err != nil {
		return errors.New("reduce retrieval worker privileges")
	}
	collectionContext, collectionCancel := context.WithTimeout(ctx, 10*time.Second)
	collectionErr := r.vector.EnsureCollection(collectionContext)
	collectionCancel()
	if collectionErr != nil {
		return errors.New("initialize vector collection")
	}
	go r.serveReadiness(ctx)
	return r.runBrokerLoop(ctx, r.runBrokerSession, initialReconnectDelay, maxReconnectDelay)
}

func (r *Runtime) runBrokerLoop(ctx context.Context, run func(context.Context) error, initialBackoff, maximumBackoff time.Duration) error {
	backoff := initialBackoff
	for ctx.Err() == nil {
		err := run(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			log.Print("retrieval worker broker session stopped; reconnecting")
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
	consumerConnection, err := dial(ctx, r.configuration.ConsumerRabbitURI)
	if err != nil {
		return errors.New("retrieval consumer broker unavailable")
	}
	defer func() { _ = consumerConnection.Close() }()
	publisherConnection, err := dial(ctx, r.configuration.PublisherRabbitURI)
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
	consumerConnectionClosed := consumerConnection.NotifyClose(make(chan *amqp091.Error, 1))
	publisherConnectionClosed := publisherConnection.NotifyClose(make(chan *amqp091.Error, 1))
	consumerChannelClosed := consumerChannel.NotifyClose(make(chan *amqp091.Error, 1))
	publisherChannelClosed := publisherChannel.NotifyClose(make(chan *amqp091.Error, 1))
	semaphore := make(chan struct{}, r.configuration.Concurrency)
	var handlers sync.WaitGroup
	defer handlers.Wait()
	dispatchTicker := time.NewTicker(500 * time.Millisecond)
	defer dispatchTicker.Stop()
	cleanupTicker := time.NewTicker(15 * time.Minute)
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
		case <-dispatchTicker.C:
			r.dispatchOutbox(sessionContext, publisher)
		case now := <-cleanupTicker.C:
			cleanupContext, cleanupCancel := context.WithTimeout(sessionContext, 30*time.Second)
			_, _ = r.repository.RecoverStaleBatches(cleanupContext, now.UTC().Add(-15*time.Minute), now.UTC())
			cleanupCancel()
		}
	}
}

func (r *Runtime) serveReadiness(ctx context.Context) {
	mux := http.NewServeMux()
	mux.HandleFunc("/readyz", func(writer http.ResponseWriter, request *http.Request) {
		probeContext, cancel := context.WithTimeout(request.Context(), 2*time.Second)
		defer cancel()
		if r.pool.Ping(probeContext) != nil || r.embedder.CheckReady(probeContext) != nil || r.vector.CheckReady(probeContext) != nil {
			http.Error(writer, "not ready", http.StatusServiceUnavailable)
			return
		}
		writer.WriteHeader(http.StatusNoContent)
	})
	server := &http.Server{Addr: r.configuration.MetricsAddress, Handler: mux, ReadHeaderTimeout: 2 * time.Second, IdleTimeout: 30 * time.Second}
	go func() {
		<-ctx.Done()
		shutdownContext, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownContext)
	}()
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Print("retrieval worker readiness listener stopped")
	}
}

func (r *Runtime) handle(ctx context.Context, semaphore chan struct{}, handlers *sync.WaitGroup, publisher retryPublisher, sourceQueue string, delivery amqp091.Delivery, handler func(context.Context, []byte) error, terminalFailure func(context.Context, []byte, error) error) {
	semaphore <- struct{}{}
	handlers.Add(1)
	go func() {
		defer handlers.Done()
		defer func() { <-semaphore }()
		if delivery.ContentType != "application/x-protobuf" || len(delivery.Body) == 0 || len(delivery.Body) > 256<<10 {
			_ = delivery.Nack(false, false)
			return
		}
		handleContext, cancel := context.WithTimeout(ctx, 3*time.Minute)
		err := handler(handleContext, delivery.Body)
		cancel()
		if err == nil {
			_ = delivery.Ack(false)
			return
		}
		if terminalFailure != nil && application.TerminalIndexingFailure(err) {
			failureContext, failureCancel := context.WithTimeout(context.Background(), 10*time.Second)
			failureErr := terminalFailure(failureContext, delivery.Body, err)
			failureCancel()
			if failureErr == nil {
				_ = delivery.Ack(false)
				return
			}
			nextAttempt, retry := failureRecordingRetryAttempt(delivery.Headers)
			if !retry {
				_ = delivery.Nack(false, false)
				return
			}
			if r.publishRetry(ctx, publisher, sourceQueue, delivery, nextAttempt) == nil {
				_ = delivery.Ack(false)
				return
			}
			_ = delivery.Nack(false, true)
			return
		}
		if errors.Is(err, application.ErrInvalidEvent) || errors.Is(err, application.ErrConflictingEvent) {
			_ = delivery.Nack(false, false)
			return
		}
		if retryAttempt(delivery.Headers) >= maximumRetryAttempts {
			if terminalFailure == nil {
				_ = delivery.Nack(false, false)
				return
			}
			failureContext, failureCancel := context.WithTimeout(context.Background(), 10*time.Second)
			failureErr := terminalFailure(failureContext, delivery.Body, err)
			failureCancel()
			if failureErr == nil {
				_ = delivery.Ack(false)
				return
			}
			_ = delivery.Nack(false, false)
			return
		}
		if r.publishRetry(ctx, publisher, sourceQueue, delivery, retryAttempt(delivery.Headers)+1) == nil {
			_ = delivery.Ack(false)
			return
		}
		_ = delivery.Nack(false, true)
	}()
}

func (r *Runtime) publishRetry(ctx context.Context, publisher retryPublisher, sourceQueue string, delivery amqp091.Delivery, attempt int64) error {
	routingKey, err := retryRoutingKey(sourceQueue, attempt)
	if err != nil {
		return err
	}
	headers := cloneHeaders(delivery.Headers)
	headers["x-retry-attempt"] = attempt
	publishContext, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return publisher.Publish(publishContext, retryExchange, routingKey, amqp091.Publishing{
		Headers: headers, ContentType: delivery.ContentType, ContentEncoding: delivery.ContentEncoding,
		DeliveryMode: amqp091.Persistent, Priority: delivery.Priority, CorrelationId: delivery.CorrelationId,
		ReplyTo: delivery.ReplyTo, Expiration: delivery.Expiration, MessageId: delivery.MessageId,
		Timestamp: delivery.Timestamp, Type: delivery.Type, UserId: delivery.UserId, AppId: delivery.AppId,
		Body: delivery.Body,
	})
}

func failureRecordingRetryAttempt(headers amqp091.Table) (int64, bool) {
	attempt := retryAttempt(headers)
	if attempt >= maximumRetryAttempts {
		return 0, false
	}
	return attempt + 1, true
}

func retryRoutingKey(sourceQueue string, attempt int64) (string, error) {
	if attempt < 1 || attempt > maximumRetryAttempts {
		return "", errors.New("invalid retry attempt")
	}
	delay := "30s"
	if attempt == 1 {
		delay = "5s"
	}
	switch sourceQueue {
	case metadataQueue, manifestQueue, batchQueue:
		return sourceQueue + ".retry." + delay, nil
	default:
		return "", errors.New("unknown retry source queue")
	}
}

func cloneHeaders(headers amqp091.Table) amqp091.Table {
	result := make(amqp091.Table, len(headers)+1)
	for key, value := range headers {
		switch key {
		case "x-death", "x-first-death-exchange", "x-first-death-queue", "x-first-death-reason", "x-delivery-count", "x-retry-attempt":
			continue
		default:
			result[key] = value
		}
	}
	return result
}

func (r *Runtime) handleMetadata(ctx context.Context, payload []byte) error {
	event, err := transport.DecodeMetadata(payload)
	if err != nil {
		return err
	}
	return r.planner.HandleMetadata(ctx, event)
}

func (r *Runtime) handleManifest(ctx context.Context, payload []byte) error {
	event, err := transport.DecodeManifestEnvelope(payload)
	if err != nil {
		return err
	}
	manifestPayload, err := r.objects.ReadBounded(ctx, event.ManifestReference, 4<<20)
	if err != nil {
		return errors.Join(errManifestArtifactRead, err)
	}
	event, err = transport.DecodeManifest(payload, manifestPayload)
	if err != nil {
		if category, terminal := application.ManifestFailureCategory(event, err); terminal {
			return r.manifestFailureRecorder().FailManifest(ctx, event, category, time.Now().UTC())
		}
		return err
	}
	return r.planner.HandleManifest(ctx, event)
}

func (r *Runtime) handleBatch(ctx context.Context, payload []byte) error {
	work, err := transport.DecodeBatch(payload)
	if err != nil {
		return err
	}
	return r.indexer.Process(ctx, work)
}

func (r *Runtime) failManifestArtifactRead(ctx context.Context, payload []byte, err error) error {
	if !errors.Is(err, errManifestArtifactRead) {
		return err
	}
	event, decodeErr := transport.DecodeManifestEnvelope(payload)
	if decodeErr != nil {
		return decodeErr
	}
	return r.manifestFailureRecorder().FailManifest(ctx, event, domain.FailureManifestIntegrity, time.Now().UTC())
}

func (r *Runtime) failBatch(ctx context.Context, payload []byte, failure error) error {
	work, err := transport.DecodeBatch(payload)
	if err != nil {
		return err
	}
	if err = r.vector.DeactivateJob(ctx, work.JobID); err != nil {
		return errors.New("deactivate failed index vectors")
	}
	return r.repository.FailBatch(ctx, work, application.FailureCategory(failure), time.Now().UTC())
}

func (r *Runtime) manifestFailureRecorder() manifestFailureRecorder {
	if r.manifestFails != nil {
		return r.manifestFails
	}
	return r.repository
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

func retryAttempt(headers amqp091.Table) int64 {
	value, found := headers["x-retry-attempt"]
	if !found {
		return deliveryAttempt(headers)
	}
	switch count := value.(type) {
	case int64:
		if count >= 0 && count <= maximumRetryAttempts {
			return count
		}
	case int32:
		if count >= 0 && int64(count) <= maximumRetryAttempts {
			return int64(count)
		}
	}
	return maximumRetryAttempts
}

func (r *Runtime) dispatchOutbox(ctx context.Context, publisher *rabbitmq.Publisher) {
	records, err := r.repository.PendingOutbox(ctx, 20, time.Now().UTC())
	if err != nil {
		return
	}
	for _, record := range records {
		publishContext, cancel := context.WithTimeout(ctx, 10*time.Second)
		publishErr := publisher.Publish(publishContext, eventExchange, record.EventType, amqp091.Publishing{
			ContentType: "application/x-protobuf", DeliveryMode: amqp091.Persistent, Type: record.EventType, MessageId: record.EventID, Timestamp: time.Now().UTC(), Body: record.Payload,
		})
		cancel()
		if publishErr != nil {
			_ = r.repository.DeferOutbox(ctx, record.EventID, time.Now().UTC())
			continue
		}
		_ = r.repository.MarkPublished(ctx, record.EventID, time.Now().UTC())
	}
}

func dial(ctx context.Context, uri string) (*amqp091.Connection, error) {
	dialer := net.Dialer{Timeout: 5 * time.Second}
	return amqp091.DialConfig(uri, amqp091.Config{Heartbeat: 10 * time.Second, Dial: func(network, address string) (net.Conn, error) {
		return dialer.DialContext(ctx, network, address)
	}})
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
