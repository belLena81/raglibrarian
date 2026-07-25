package diagnostic

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"
	"time"
)

func TestLoggerEmitsDeferredAndCompletionDiagnostics(t *testing.T) {
	var output bytes.Buffer
	logger := New(slog.New(slog.NewJSONHandler(&output, nil)))
	retryAt := time.Date(2026, time.July, 25, 10, 42, 4, 0, time.UTC)

	logger.ProcessingDeferred("event-1", "book-1", "processing_error", "extract_failed", retryAt)
	logger.ReadyEventPrepared("event-1", "book-1", "ready-1")
	logger.CompletionPersisted("event-1", "book-1", "ready-1")
	logger.CompletionPersistenceFailed("event-1", "book-1", "ready-1", "complete_failed")
	logger.OutboxPublished("ready-1", "book-1", "ingestion.book.chunks-ready.v1")
	logger.OutboxDeferred("ready-1", "book-1", "ingestion.book.chunks-ready.v1", "publish_failed")
	logger.OutboxMarkedPublished("ready-1", "book-1", "ingestion.book.chunks-ready.v1")

	lines := bytes.Split(bytes.TrimSpace(output.Bytes()), []byte("\n"))
	if len(lines) != 7 {
		t.Fatalf("log line count = %d, want 7", len(lines))
	}

	assertField := func(index int, key string, want any) {
		t.Helper()
		var payload map[string]any
		if err := json.Unmarshal(lines[index], &payload); err != nil {
			t.Fatalf("json.Unmarshal(line %d) error = %v", index, err)
		}
		if payload[key] != want {
			t.Fatalf("line %d %s = %v, want %v", index, key, payload[key], want)
		}
	}

	assertField(0, "msg", "ingestion processing deferred")
	assertField(0, "reason_detail", "extract_failed")
	assertField(0, "retry_at", "2026-07-25T10:42:04Z")
	assertField(1, "ready_event_id", "ready-1")
	assertField(2, "msg", "ingestion completion persisted")
	assertField(3, "reason_code", "complete_failed")
	assertField(4, "event_type", "ingestion.book.chunks-ready.v1")
	assertField(5, "reason_code", "publish_failed")
	assertField(6, "msg", "ingestion outbox marked_published")
}
