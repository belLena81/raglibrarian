package worker

import (
	"context"
	"errors"
	"testing"
	"time"

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
