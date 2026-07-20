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
