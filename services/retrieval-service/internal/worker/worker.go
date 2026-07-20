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
	initialReconnectDelay = time.Second
	maxReconnectDelay     = 30 * time.Second
)

type Runtime struct {
	configuration config.WorkerConfig
	pool          *pgxpool.Pool
	repository    *repository.Postgres
	objects       *storage.MinIO
	planner       *application.Planner
	indexer       *application.Indexer
	embedder      *embedding.TEI
	vector        *vector.Qdrant
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
	return &Runtime{configuration: configuration, pool: pool, repository: records, objects: objects, planner: planner, indexer: indexer, embedder: embedder, vector: index}, nil
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
			r.handle(sessionContext, semaphore, &handlers, delivery, r.handleMetadata, nil)
		case delivery, open := <-manifestDeliveries:
			if !open {
				sessionCancel()
				return errors.New("manifest delivery channel closed")
			}
			r.handle(sessionContext, semaphore, &handlers, delivery, r.handleManifest, nil)
		case delivery, open := <-batchDeliveries:
			if !open {
				sessionCancel()
				return errors.New("batch delivery channel closed")
			}
			r.handle(sessionContext, semaphore, &handlers, delivery, r.handleBatch, r.failBatch)
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

func (r *Runtime) handle(ctx context.Context, semaphore chan struct{}, handlers *sync.WaitGroup, delivery amqp091.Delivery, handler func(context.Context, []byte) error, terminalFailure func(context.Context, []byte) error) {
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
		if errors.Is(err, application.ErrInvalidEvent) || errors.Is(err, application.ErrConflictingEvent) || errors.Is(err, application.ErrUnsupportedIndexProfile) {
			_ = delivery.Nack(false, false)
			return
		}
		if terminalFailure != nil && deliveryAttempt(delivery.Headers) >= 4 {
			failureContext, failureCancel := context.WithTimeout(context.Background(), 10*time.Second)
			failureErr := terminalFailure(failureContext, delivery.Body)
			failureCancel()
			if failureErr == nil {
				_ = delivery.Ack(false)
				return
			}
		}
		_ = delivery.Nack(false, true)
	}()
}

func (r *Runtime) handleMetadata(ctx context.Context, payload []byte) error {
	event, err := transport.DecodeMetadata(payload)
	if err != nil {
		return err
	}
	return r.planner.HandleMetadata(ctx, event)
}

func (r *Runtime) handleManifest(ctx context.Context, payload []byte) error {
	reference, err := transport.ManifestReference(payload)
	if err != nil {
		return err
	}
	manifestPayload, err := r.objects.ReadBounded(ctx, reference, 4<<20)
	if err != nil {
		return err
	}
	event, err := transport.DecodeManifest(payload, manifestPayload)
	if err != nil {
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

func (r *Runtime) failBatch(ctx context.Context, payload []byte) error {
	work, err := transport.DecodeBatch(payload)
	if err != nil {
		return err
	}
	return r.repository.FailBatch(ctx, work, domain.FailureInternalIndexing, time.Now().UTC())
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
