package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	ingestionv1 "github.com/belLena81/raglibrarian/pkg/proto/ingestion/v1"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/application"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/domain"
	"github.com/rabbitmq/amqp091-go"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestDeliveryAttemptParsesBoundedBrokerHeader(t *testing.T) {
	if got := deliveryAttempt(amqp091.Table{"x-delivery-count": int64(4)}); got != 4 {
		t.Fatalf("deliveryAttempt() = %d", got)
	}
	if got := deliveryAttempt(amqp091.Table{"x-delivery-count": "invalid"}); got != 5 {
		t.Fatalf("invalid deliveryAttempt() = %d", got)
	}
}

func TestRetryRoutingUsesQueueSpecificDelayedLanes(t *testing.T) {
	tests := []struct {
		queue   string
		attempt int64
		want    string
	}{
		{metadataQueue, 1, "retrieval.book-uploaded.v1.retry.5s"},
		{metadataQueue, 2, "retrieval.book-uploaded.v1.retry.30s"},
		{manifestQueue, 1, "retrieval.chunks-ready.v1.retry.5s"},
		{batchQueue, 4, "retrieval.index-batch.v1.retry.30s"},
	}
	for _, test := range tests {
		got, err := retryRoutingKey(test.queue, test.attempt)
		if err != nil || got != test.want {
			t.Fatalf("retryRoutingKey(%q, %d) = %q, %v", test.queue, test.attempt, got, err)
		}
	}
	if _, err := retryRoutingKey("unknown", 1); err == nil {
		t.Fatal("unknown queue accepted")
	}
}

func TestRetryAttemptDoesNotTrustMalformedHeaders(t *testing.T) {
	if got := retryAttempt(amqp091.Table{"x-retry-attempt": int64(3)}); got != 3 {
		t.Fatalf("retryAttempt() = %d", got)
	}
	if got := retryAttempt(amqp091.Table{"x-retry-attempt": "invalid"}); got != maximumRetryAttempts {
		t.Fatalf("invalid retryAttempt() = %d", got)
	}
	headers := cloneHeaders(amqp091.Table{"x-death": "broker", "x-retry-attempt": int64(1), "trace": "keep"})
	if _, found := headers["x-death"]; found || headers["trace"] != "keep" {
		t.Fatalf("headers were not sanitized: %#v", headers)
	}
}

func TestHandleDeadLettersExhaustedTerminalFailureRecording(t *testing.T) {
	acknowledger := &stubAcknowledger{}
	publisher := &stubRetryPublisher{}
	delivery := amqp091.Delivery{Acknowledger: acknowledger, DeliveryTag: 1, ContentType: "application/x-protobuf", Body: []byte{1}, Headers: amqp091.Table{"x-retry-attempt": maximumRetryAttempts}}
	semaphore := make(chan struct{}, 1)
	var handlers sync.WaitGroup

	(&Runtime{}).handle(context.Background(), semaphore, &handlers, publisher, batchQueue, delivery, func(context.Context, []byte) error {
		return application.Failure(domain.FailureManifestIntegrity, errors.New("malformed shard"))
	}, func(context.Context, []byte, error) error {
		return errors.New("qdrant unavailable")
	})
	handlers.Wait()

	if acknowledger.acks != 0 || acknowledger.nacks != 1 || acknowledger.requeue {
		t.Fatalf("settlement acks=%d nacks=%d requeue=%v", acknowledger.acks, acknowledger.nacks, acknowledger.requeue)
	}
	if len(publisher.messages) != 0 {
		t.Fatalf("published retries = %d, want 0", len(publisher.messages))
	}
}

func TestHandleRetriesTerminalFailureRecordingBelowBudget(t *testing.T) {
	acknowledger := &stubAcknowledger{}
	publisher := &stubRetryPublisher{}
	delivery := amqp091.Delivery{Acknowledger: acknowledger, DeliveryTag: 1, ContentType: "application/x-protobuf", Body: []byte{1}, Headers: amqp091.Table{"x-retry-attempt": int64(1)}}
	semaphore := make(chan struct{}, 1)
	var handlers sync.WaitGroup

	(&Runtime{}).handle(context.Background(), semaphore, &handlers, publisher, batchQueue, delivery, func(context.Context, []byte) error {
		return application.Failure(domain.FailureManifestIntegrity, errors.New("malformed shard"))
	}, func(context.Context, []byte, error) error {
		return errors.New("qdrant unavailable")
	})
	handlers.Wait()

	if acknowledger.acks != 1 || acknowledger.nacks != 0 {
		t.Fatalf("settlement acks=%d nacks=%d", acknowledger.acks, acknowledger.nacks)
	}
	if len(publisher.messages) != 1 || publisher.messages[0].RoutingKey != "retrieval.index-batch.v1.retry.30s" || publisher.messages[0].Headers["x-retry-attempt"] != int64(2) {
		t.Fatalf("published retry = %#v", publisher.messages)
	}
}

func TestHandleDeadLettersExhaustedRetryWhenFailureRecordingFails(t *testing.T) {
	acknowledger := &stubAcknowledger{}
	publisher := &stubRetryPublisher{}
	delivery := amqp091.Delivery{Acknowledger: acknowledger, DeliveryTag: 1, ContentType: "application/x-protobuf", Body: []byte{1}, Headers: amqp091.Table{"x-retry-attempt": maximumRetryAttempts}}
	semaphore := make(chan struct{}, 1)
	var handlers sync.WaitGroup

	(&Runtime{}).handle(context.Background(), semaphore, &handlers, publisher, batchQueue, delivery, func(context.Context, []byte) error {
		return errors.New("transient dependency unavailable")
	}, func(context.Context, []byte, error) error {
		return errors.New("database unavailable")
	})
	handlers.Wait()

	if acknowledger.acks != 0 || acknowledger.nacks != 1 || acknowledger.requeue {
		t.Fatalf("settlement acks=%d nacks=%d requeue=%v", acknowledger.acks, acknowledger.nacks, acknowledger.requeue)
	}
	if len(publisher.messages) != 0 {
		t.Fatalf("published retries = %d, want 0", len(publisher.messages))
	}
}

func TestHandleRetriesManifestReadFailureBelowBudget(t *testing.T) {
	acknowledger := &stubAcknowledger{}
	publisher := &stubRetryPublisher{}
	delivery := amqp091.Delivery{Acknowledger: acknowledger, DeliveryTag: 1, ContentType: "application/x-protobuf", Body: validWorkerManifestPayload(t), Headers: amqp091.Table{"x-retry-attempt": int64(1)}}
	semaphore := make(chan struct{}, 1)
	var handlers sync.WaitGroup
	recorder := &stubManifestFailureRecorder{}
	runtime := &Runtime{objects: stubObjectStore{readErr: errors.New("artifact exceeds limit")}, manifestFails: recorder}

	runtime.handle(context.Background(), semaphore, &handlers, publisher, manifestQueue, delivery, runtime.handleManifest, runtime.failManifestArtifactRead)
	handlers.Wait()

	if acknowledger.acks != 1 || acknowledger.nacks != 0 {
		t.Fatalf("settlement acks=%d nacks=%d", acknowledger.acks, acknowledger.nacks)
	}
	if len(publisher.messages) != 1 || publisher.messages[0].RoutingKey != "retrieval.chunks-ready.v1.retry.30s" || publisher.messages[0].Headers["x-retry-attempt"] != int64(2) {
		t.Fatalf("published retry = %#v", publisher.messages)
	}
	if recorder.calls != 0 {
		t.Fatalf("terminal manifest failures = %d, want 0", recorder.calls)
	}
}

func TestHandleRecordsManifestReadFailureAfterRetryExhaustion(t *testing.T) {
	acknowledger := &stubAcknowledger{}
	publisher := &stubRetryPublisher{}
	delivery := amqp091.Delivery{Acknowledger: acknowledger, DeliveryTag: 1, ContentType: "application/x-protobuf", Body: validWorkerManifestPayload(t), Headers: amqp091.Table{"x-retry-attempt": maximumRetryAttempts}}
	semaphore := make(chan struct{}, 1)
	var handlers sync.WaitGroup
	recorder := &stubManifestFailureRecorder{}
	runtime := &Runtime{objects: stubObjectStore{readErr: errors.New("artifact exceeds limit")}, manifestFails: recorder}

	runtime.handle(context.Background(), semaphore, &handlers, publisher, manifestQueue, delivery, runtime.handleManifest, runtime.failManifestArtifactRead)
	handlers.Wait()

	if acknowledger.acks != 1 || acknowledger.nacks != 0 {
		t.Fatalf("settlement acks=%d nacks=%d", acknowledger.acks, acknowledger.nacks)
	}
	if len(publisher.messages) != 0 {
		t.Fatalf("published retries = %d, want 0", len(publisher.messages))
	}
	if recorder.calls != 1 || recorder.category != domain.FailureManifestIntegrity || recorder.event.BookID != "book-1" || recorder.event.ManifestReference == "" {
		t.Fatalf("recorded manifest failure calls=%d category=%q event=%#v", recorder.calls, recorder.category, recorder.event)
	}
	if len(recorder.event.Manifest.Shards) != 0 {
		t.Fatal("recorded read failure retained manifest artifact payload")
	}
}

func TestHandleDoesNotRecordManifestIntegrityForPlannerFailure(t *testing.T) {
	acknowledger := &stubAcknowledger{}
	publisher := &stubRetryPublisher{}
	delivery := amqp091.Delivery{Acknowledger: acknowledger, DeliveryTag: 1, ContentType: "application/x-protobuf", Body: validWorkerManifestPayload(t), Headers: amqp091.Table{"x-retry-attempt": maximumRetryAttempts}}
	semaphore := make(chan struct{}, 1)
	var handlers sync.WaitGroup
	recorder := &stubManifestFailureRecorder{}
	runtime := &Runtime{manifestFails: recorder}

	runtime.handle(context.Background(), semaphore, &handlers, publisher, manifestQueue, delivery, func(context.Context, []byte) error {
		return errors.New("database unavailable")
	}, runtime.failManifestArtifactRead)
	handlers.Wait()

	if acknowledger.acks != 0 || acknowledger.nacks != 1 || acknowledger.requeue {
		t.Fatalf("settlement acks=%d nacks=%d requeue=%v", acknowledger.acks, acknowledger.nacks, acknowledger.requeue)
	}
	if recorder.calls != 0 {
		t.Fatalf("terminal manifest failures = %d, want 0", recorder.calls)
	}
}

func TestBrokerLoopReconnectsAfterSessionFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	attempts := 0

	err := (&Runtime{}).runBrokerLoop(ctx, func(context.Context) error {
		attempts++
		if attempts == 2 {
			cancel()
		}
		return errors.New("broker session closed")
	}, time.Millisecond, time.Millisecond)

	if err != nil {
		t.Fatalf("runBrokerLoop() error = %v", err)
	}
	if attempts != 2 {
		t.Fatalf("runBrokerLoop() attempts = %d, want 2", attempts)
	}
}

type publishedRetry struct {
	Exchange   string
	RoutingKey string
	Headers    amqp091.Table
}

type stubRetryPublisher struct {
	messages []publishedRetry
	err      error
}

type stubObjectStore struct {
	payload []byte
	readErr error
}

func (s stubObjectStore) Open(context.Context, string) (io.ReadCloser, int64, error) {
	return nil, 0, errors.New("not implemented")
}

func (s stubObjectStore) ReadBounded(context.Context, string, int64) ([]byte, error) {
	if s.readErr != nil {
		return nil, s.readErr
	}
	return append([]byte(nil), s.payload...), nil
}

type stubManifestFailureRecorder struct {
	calls    int
	event    application.ManifestEvent
	category domain.FailureCategory
}

func (s *stubManifestFailureRecorder) FailManifest(_ context.Context, event application.ManifestEvent, category domain.FailureCategory, _ time.Time) error {
	s.calls++
	s.event = event
	s.category = category
	return nil
}

func validWorkerManifestPayload(t *testing.T) []byte {
	t.Helper()
	source := sha256.Sum256([]byte("synthetic source"))
	processing := sha256.Sum256([]byte("processing profile"))
	manifest := &ingestionv1.ChunkManifestV1{
		SchemaVersion:          "v1",
		BookId:                 "book-1",
		SourceSha256:           source[:],
		ProcessingConfigDigest: processing[:],
		ExtractionVersion:      "poppler-layout-v1",
		NormalizationVersion:   "nfc-v1",
		TokenizerVersion:       "cl100k_base-v1",
		ChunkingVersion:        "token-window-v2",
		StructureVersion:       "heading-carry-v1",
		MaximumTokens:          800,
		OverlapTokens:          120,
		PageCount:              1,
		ChunkCount:             1,
		GeneratedAt:            timestamppb.New(time.Date(2026, 7, 20, 9, 0, 0, 0, time.UTC)),
	}
	manifestPayload, err := proto.MarshalOptions{Deterministic: true}.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifestDigest := sha256.Sum256(manifestPayload)
	directory := "books/book-1/" + hex.EncodeToString(source[:]) + "/" + hex.EncodeToString(processing[:]) + "/"
	outer := &ingestionv1.BookChunksReadyV1{
		EventId:              "event-1",
		BookId:               "book-1",
		SourceSha256:         source[:],
		ManifestReference:    directory + "manifest.pb",
		ManifestSha256:       manifestDigest[:],
		ManifestByteSize:     int64(len(manifestPayload)),
		PageCount:            1,
		ChunkCount:           1,
		ExtractionVersion:    manifest.ExtractionVersion,
		NormalizationVersion: manifest.NormalizationVersion,
		TokenizerVersion:     manifest.TokenizerVersion,
		ChunkingVersion:      manifest.ChunkingVersion,
		StructureVersion:     manifest.StructureVersion,
		MaximumTokens:        manifest.MaximumTokens,
		OverlapTokens:        manifest.OverlapTokens,
		CorrelationId:        "correlation-1",
		OccurredAt:           timestamppb.New(time.Date(2026, 7, 20, 9, 1, 0, 0, time.UTC)),
		CausationId:          "cause-1",
		Producer:             "ingestion-service",
		SchemaVersion:        "v1",
		IdempotencyKey:       "book-1:" + hex.EncodeToString(processing[:]) + ":ready",
	}
	payload, err := proto.MarshalOptions{Deterministic: true}.Marshal(outer)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func (s *stubRetryPublisher) Publish(_ context.Context, exchange, routingKey string, message amqp091.Publishing) error {
	if s.err != nil {
		return s.err
	}
	s.messages = append(s.messages, publishedRetry{Exchange: exchange, RoutingKey: routingKey, Headers: message.Headers})
	return nil
}

type stubAcknowledger struct {
	acks    int
	nacks   int
	requeue bool
}

func (s *stubAcknowledger) Ack(uint64, bool) error {
	s.acks++
	return nil
}

func (s *stubAcknowledger) Nack(_ uint64, _ bool, requeue bool) error {
	s.nacks++
	s.requeue = requeue
	return nil
}

func (s *stubAcknowledger) Reject(_ uint64, requeue bool) error {
	s.nacks++
	s.requeue = requeue
	return nil
}
