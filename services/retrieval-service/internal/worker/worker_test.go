package worker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/application"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/domain"
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

func TestAwaitReadinessReturnsNilWhenDependencyBecomesReady(t *testing.T) {
	attempts := 0
	err := awaitReadiness(context.Background(), func(context.Context) error {
		attempts++
		if attempts < 3 {
			return errors.New("not ready yet")
		}
		return nil
	}, 5, time.Millisecond, 4*time.Millisecond, 2*time.Millisecond)
	if err != nil {
		t.Fatalf("awaitReadiness() error = %v", err)
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3", attempts)
	}
}

func TestAwaitReadinessReturnsContextCancelationError(t *testing.T) {
	attempts := 0
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := awaitReadiness(ctx, func(context.Context) error {
		attempts++
		return errors.New("not ready")
	}, 5, time.Millisecond, 4*time.Millisecond, 2*time.Millisecond)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("awaitReadiness() error = %v, want %v", err, context.Canceled)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
}

func TestAwaitReadinessRejectsInvalidProbe(t *testing.T) {
	err := awaitReadiness(context.Background(), nil, 5, time.Millisecond, 4*time.Millisecond, 2*time.Millisecond)
	if err == nil {
		t.Fatal("awaitReadiness() error = nil, want invalid readiness probe")
	}
	err = awaitReadiness(context.Background(), func(context.Context) error { return errors.New("not ready") }, 0, time.Millisecond, 4*time.Millisecond, 2*time.Millisecond)
	if err == nil {
		t.Fatal("awaitReadiness() error = nil, want invalid readiness probe")
	}
}

func TestRejectionReasonMapsFailureCategoryAndCommonErrors(t *testing.T) {
	if got := rejectionReason(application.Failure(domain.FailureEmbeddingUnavailable, errors.New("tei unavailable"))); got != "embedding_unavailable" {
		t.Fatalf("rejectionReason() = %q, want %q", got, "embedding_unavailable")
	}
	if got := rejectionReason(application.Failure(domain.FailureIndexingTimeout, errors.New("tei timeout"))); got != "indexing_timeout" {
		t.Fatalf("rejectionReason() = %q, want %q", got, "indexing_timeout")
	}
	if got := rejectionReason(application.ErrConflictingEvent); got != "conflicting_event" {
		t.Fatalf("rejectionReason() = %q, want %q", got, "conflicting_event")
	}
	if got := rejectionReason(application.ErrInvalidEvent); got != "invalid_event" {
		t.Fatalf("rejectionReason() = %q, want %q", got, "invalid_event")
	}
	if got := rejectionReason(errors.New("other failure")); got != "unknown_failure" {
		t.Fatalf("rejectionReason() = %q, want %q", got, "unknown_failure")
	}
}
