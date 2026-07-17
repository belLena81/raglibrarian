package outbox

import (
	"context"
	"testing"

	"github.com/rabbitmq/amqp091-go"
)

func TestAwaitRoutedConfirmationRejectsReturnBeforeACK(t *testing.T) {
	returns := make(chan amqp091.Return, 1)
	confirmations := make(chan amqp091.Confirmation, 1)
	returns <- amqp091.Return{MessageId: "event-1"}
	confirmations <- amqp091.Confirmation{DeliveryTag: 1, Ack: true}
	publisher := &RabbitPublisher{returns: returns, confirmations: confirmations}

	if err := publisher.awaitRoutedConfirmation(context.Background(), 1, "event-1"); err == nil {
		t.Fatal("expected mandatory return to reject publication")
	}
}

func TestAwaitRoutedConfirmationRejectsUnexpectedReturn(t *testing.T) {
	returns := make(chan amqp091.Return, 1)
	confirmations := make(chan amqp091.Confirmation, 1)
	returns <- amqp091.Return{MessageId: "another-event"}
	confirmations <- amqp091.Confirmation{DeliveryTag: 1, Ack: true}
	publisher := &RabbitPublisher{returns: returns, confirmations: confirmations}

	if err := publisher.awaitRoutedConfirmation(context.Background(), 1, "event-1"); err == nil {
		t.Fatal("expected unexpected mandatory return to reject publication")
	}
}

func TestAwaitRoutedConfirmationAcceptsMatchingACKWithoutReturn(t *testing.T) {
	returns := make(chan amqp091.Return, 1)
	confirmations := make(chan amqp091.Confirmation, 1)
	confirmations <- amqp091.Confirmation{DeliveryTag: 1, Ack: true}
	publisher := &RabbitPublisher{returns: returns, confirmations: confirmations}

	if err := publisher.awaitRoutedConfirmation(context.Background(), 1, "event-1"); err != nil {
		t.Fatalf("awaitRoutedConfirmation() error = %v", err)
	}
}

func TestAwaitRoutedConfirmationRejectsNack(t *testing.T) {
	returns := make(chan amqp091.Return, 1)
	confirmations := make(chan amqp091.Confirmation, 1)
	confirmations <- amqp091.Confirmation{DeliveryTag: 1, Ack: false}
	publisher := &RabbitPublisher{returns: returns, confirmations: confirmations}

	if err := publisher.awaitRoutedConfirmation(context.Background(), 1, "event-1"); err == nil {
		t.Fatal("expected nack to reject publication")
	}
}
