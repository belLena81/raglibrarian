package worker

import (
	"testing"

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
