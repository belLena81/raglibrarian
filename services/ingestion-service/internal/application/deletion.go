package application

import (
	"context"
	"crypto/sha256"
	"time"
)

// DeletionEvent requests cleanup of every artifact generation at or before the
// Catalog lifecycle version. Catalog remains the lifecycle authority.
type DeletionEvent struct {
	EventID          string
	BookID           string
	CommandID        string
	LifecycleVersion int64
	CorrelationID    string
	CausationID      string
	Producer         string
	SchemaVersion    string
	IdempotencyKey   string
	OccurredAt       time.Time
	Payload          []byte
}

func (e DeletionEvent) Validate() error {
	if !safeID(e.EventID) || !safeID(e.BookID) || !safeID(e.CommandID) ||
		!safeID(e.CorrelationID) || !safeID(e.CausationID) ||
		e.IdempotencyKey != e.CommandID || e.Producer != "catalog-service" ||
		e.SchemaVersion != "v1" || e.LifecycleVersion < 1 ||
		e.OccurredAt.IsZero() || len(e.Payload) == 0 || len(e.Payload) > 256<<10 {
		return ErrInvalidEvent
	}
	return nil
}

// ProcessDeletion durably fences the lifecycle before the delivery is
// acknowledged. Exact-prefix storage cleanup and the acknowledgment outbox are
// completed asynchronously by the artifact cleaner.
func (p *Processor) ProcessDeletion(ctx context.Context, event DeletionEvent) error {
	if err := event.Validate(); err != nil {
		return err
	}
	now := p.now().UTC()
	ack, err := p.events.ArtifactsDeleted(event, now)
	if err != nil {
		return err
	}
	payloadDigest := sha256.Sum256(event.Payload)
	if err = p.repository.AcceptDeletion(ctx, event, payloadDigest, ack, now); err != nil {
		return operational("accept_deletion_failed", err)
	}
	return nil
}
