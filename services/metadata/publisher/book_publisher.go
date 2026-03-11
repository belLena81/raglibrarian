// Package publisher defines the outbound messaging port for book domain events.
package publisher

import "context"

// EventType identifies which domain event occurred.
// Using typed constants (not bare strings) keeps routing keys consistent between
// the publisher implementation and any consumer that pattern-matches on them.
type EventType string

const (
	// EventBookCreated is emitted after a new book is persisted in StatusPending.
	// The ingest Lambda subscribes to this to start the PDF pipeline.
	EventBookCreated EventType = "book.created"

	// EventBookReindexRequested is emitted after a terminal book is reset to
	// StatusPending. The Lambda re-triggers the same pipeline as for a new book.
	EventBookReindexRequested EventType = "book.reindex_requested"
)

// BookEvent is the payload published to RabbitMQ for all book domain events.
// Fields are kept flat (no nesting) so the Lambda can route and log without
// deserialising nested objects.
//
// occurred_at is RFC 3339 UTC — human-readable and unambiguous in logs.
// s3_key may be empty for EventBookCreated (the key is set by the Lambda after
// upload); it is populated for EventBookReindexRequested when the PDF already
// exists.
type BookEvent struct {
	Event      EventType `json:"event"`
	BookID     string    `json:"book_id"`
	S3Key      string    `json:"s3_key,omitempty"`
	OccurredAt string    `json:"occurred_at"` // RFC 3339 UTC
}

// BookPublisher is the outbound messaging port.
// Implementations must be safe for concurrent use.
type BookPublisher interface {
	// Publish sends a BookEvent to the message broker.
	// Returns an error only for infrastructure failures (connection lost,
	// serialisation error). Business validation is not performed here.
	Publish(ctx context.Context, event BookEvent) error

	// Close releases the underlying channel and connection.
	// Must be called once when the process is shutting down.
	Close() error
}
