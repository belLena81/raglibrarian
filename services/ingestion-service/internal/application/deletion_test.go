package application

import (
	"context"
	"testing"
	"time"
)

func TestDeletionEventValidateRequiresCatalogCommandIdentity(t *testing.T) {
	event := validDeletionEvent()
	if err := event.Validate(); err != nil {
		t.Fatalf("valid event: %v", err)
	}
	event.IdempotencyKey = event.BookID
	if err := event.Validate(); err == nil {
		t.Fatal("expected command idempotency validation")
	}
}

func TestProcessDeletionPersistsFenceBeforeAcknowledgement(t *testing.T) {
	processor, repository, _, _, _ := newTestProcessor(t, processorOptions{})
	event := validDeletionEvent()
	if err := processor.ProcessDeletion(context.Background(), event); err != nil {
		t.Fatalf("process deletion: %v", err)
	}
	if repository.deletionCalls != 1 {
		t.Fatalf("deletion calls = %d, want 1", repository.deletionCalls)
	}
}

func validDeletionEvent() DeletionEvent {
	return DeletionEvent{
		EventID:          "delete-event",
		BookID:           "book-1",
		CommandID:        "delete-command",
		LifecycleVersion: 2,
		CorrelationID:    "correlation-1",
		CausationID:      "delete-command",
		Producer:         "catalog-service",
		SchemaVersion:    "v1",
		IdempotencyKey:   "delete-command",
		OccurredAt:       time.Unix(1_700_000_000, 0).UTC(),
		Payload:          []byte{1},
	}
}
