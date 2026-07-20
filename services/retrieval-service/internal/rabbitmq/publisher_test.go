package rabbitmq

import (
	"context"
	"errors"
	"testing"

	"github.com/rabbitmq/amqp091-go"
)

func TestWaitMandatoryConfirmationFailsWhenEventIsReturned(t *testing.T) {
	returns := make(chan amqp091.Return, 1)
	returns <- amqp091.Return{Exchange: "raglibrarian.retrieval.events.v1", RoutingKey: "retrieval.book.indexed.v1"}

	err := waitMandatoryConfirmation(context.Background(), returns, func(context.Context) (bool, error) {
		return true, nil
	})

	if !errors.Is(err, ErrEventReturned) {
		t.Fatalf("waitMandatoryConfirmation() error = %v, want %v", err, ErrEventReturned)
	}
}

func TestWaitMandatoryConfirmationRequiresBrokerAck(t *testing.T) {
	err := waitMandatoryConfirmation(context.Background(), make(chan amqp091.Return, 1), func(context.Context) (bool, error) {
		return false, nil
	})

	if !errors.Is(err, ErrEventNotConfirmed) {
		t.Fatalf("waitMandatoryConfirmation() error = %v, want %v", err, ErrEventNotConfirmed)
	}
}

func TestWaitMandatoryConfirmationPropagatesWaitError(t *testing.T) {
	waitErr := errors.New("confirm channel closed")

	err := waitMandatoryConfirmation(context.Background(), make(chan amqp091.Return, 1), func(context.Context) (bool, error) {
		return false, waitErr
	})

	if !errors.Is(err, waitErr) {
		t.Fatalf("waitMandatoryConfirmation() error = %v, want %v", err, waitErr)
	}
}
