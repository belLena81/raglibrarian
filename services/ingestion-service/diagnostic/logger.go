// Package diagnostic provides allowlisted, content-free operational logging.
package diagnostic

import (
	"log/slog"
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
func (l *Logger) ProcessingFailed(eventID, bookID, category string) {
	l.value.Warn("ingestion processing failed", "event_id", eventID, "book_id", bookID, "failure_category", category)
}
func (l *Logger) DependencyUnavailable(dependency string) {
	l.value.Warn("ingestion dependency unavailable", "dependency", dependency)
}
