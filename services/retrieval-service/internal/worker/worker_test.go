package worker

import (
	"context"
	"errors"
	"testing"

	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/application"
	"github.com/rabbitmq/amqp091-go"
)

func TestProcessOneRejectsUnknownQueueBeforeUsingRuntimeDependencies(t *testing.T) {
	runtime := &Runtime{}
	err := runtime.ProcessOne(context.Background(), "unknown", "event", []byte("payload"))
	if !errors.Is(err, application.ErrInvalidEvent) {
		t.Fatalf("ProcessOne() error = %v, want invalid event", err)
	}
}

func TestProcessOneDeliveryLeavesCancelledDeliveryUnsettled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := (&Runtime{}).ProcessOneDelivery(ctx, nil, metadataQueue, amqp091.Delivery{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ProcessOneDelivery() error = %v, want cancelled", err)
	}
}

func TestProcessOneRejectsWrongEventTypeBeforeUsingRuntimeDependencies(t *testing.T) {
	runtime := &Runtime{}
	err := runtime.ProcessOne(context.Background(), metadataQueue, "unexpected", []byte("payload"))
	if !errors.Is(err, application.ErrInvalidEvent) {
		t.Fatalf("ProcessOne() error = %v, want invalid event", err)
	}
}
