// Package diagnostic provides allowlisted, content-free operational logging.
package diagnostic

import (
	"log/slog"
	"time"
)

type Logger struct{ value *slog.Logger }

func New(value *slog.Logger) *Logger {
	if value == nil {
		value = slog.Default()
	}
	return &Logger{value: value}
}

func (l *Logger) ProcessingStarted(eventID, bookID string) {
	l.value.Info("ingestion processing started", "event_id", eventID, "book_id", bookID)
}
func (l *Logger) ProcessingCompleted(eventID, bookID string) {
	l.value.Info("ingestion processing completed", "event_id", eventID, "book_id", bookID)
}

func (l *Logger) ProcessingDeferred(eventID, bookID, reason, detail string, retryAt time.Time) {
	fields := []any{"event_id", eventID, "book_id", bookID, "reason_code", reason}
	if detail != "" {
		fields = append(fields, "reason_detail", detail)
	}
	if !retryAt.IsZero() {
		fields = append(fields, "retry_at", retryAt)
	}
	l.value.Warn("ingestion processing deferred", fields...)
}

func (l *Logger) ProcessingFailed(eventID, bookID, category, detail string) {
	fields := []any{"event_id", eventID, "book_id", bookID, "failure_category", category}
	if detail != "" {
		fields = append(fields, "reason_detail", detail)
	}
	l.value.Warn("ingestion processing failed", fields...)
}

func (l *Logger) ReadyEventPrepared(eventID, bookID, readyEventID string) {
	l.value.Info("ingestion ready event prepared", "event_id", eventID, "book_id", bookID, "ready_event_id", readyEventID)
}

func (l *Logger) CompletionPersisted(eventID, bookID, readyEventID string) {
	l.value.Info("ingestion completion persisted", "event_id", eventID, "book_id", bookID, "ready_event_id", readyEventID)
}

func (l *Logger) CompletionPersistenceFailed(eventID, bookID, readyEventID, reason string) {
	l.value.Warn("ingestion completion persistence failed", "event_id", eventID, "book_id", bookID, "ready_event_id", readyEventID, "reason_code", reason)
}

func (l *Logger) FailurePersisted(eventID, bookID, failedEventID, category, detail string) {
	fields := []any{"event_id", eventID, "book_id", bookID, "failed_event_id", failedEventID, "failure_category", category}
	if detail != "" {
		fields = append(fields, "reason_detail", detail)
	}
	l.value.Info("ingestion failure persisted", fields...)
}

func (l *Logger) OutboxPublished(eventID, aggregateID, eventType string) {
	l.value.Info("ingestion outbox published", "event_id", eventID, "aggregate_id", aggregateID, "event_type", eventType)
}

func (l *Logger) OutboxDeferred(eventID, aggregateID, eventType, reason string) {
	l.value.Warn("ingestion outbox deferred", "event_id", eventID, "aggregate_id", aggregateID, "event_type", eventType, "reason_code", reason)
}

func (l *Logger) OutboxMarkedPublished(eventID, aggregateID, eventType string) {
	l.value.Info("ingestion outbox marked_published", "event_id", eventID, "aggregate_id", aggregateID, "event_type", eventType)
}

func (l *Logger) DependencyUnavailable(dependency string) {
	l.value.Warn("ingestion dependency unavailable", "dependency", dependency)
}
