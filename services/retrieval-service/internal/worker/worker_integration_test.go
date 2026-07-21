package worker

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/application"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/domain"
	"github.com/rabbitmq/amqp091-go"
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
	}, func(context.Context, []byte, domain.FailureCategory) error {
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
	}, func(context.Context, []byte, domain.FailureCategory) error {
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
	}, func(context.Context, []byte, domain.FailureCategory) error {
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
