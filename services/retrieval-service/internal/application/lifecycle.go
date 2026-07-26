package application

import (
	"context"
	"errors"
	"strings"
	"time"
)

type LifecycleKind string

const (
	LifecycleReindex LifecycleKind = "reindex"
	LifecycleDelete  LifecycleKind = "delete"
)

// LifecycleEvent is the transport-independent form of Catalog lifecycle
// commands. ActorID is retained for audit only; authorization remains a
// Catalog responsibility and is never inferred from this event.
type LifecycleEvent struct {
	EventID, BookID, CommandID, ActorID, CorrelationID, CausationID, Producer, SchemaVersion, IdempotencyKey string
	ManifestReference                                                                                        string
	Kind                                                                                                     LifecycleKind
	LifecycleVersion                                                                                         uint64
	SourceSHA256, ManifestSHA256, PayloadDigest                                                              [32]byte
	OccurredAt                                                                                               time.Time
}

func (e LifecycleEvent) Validate() error {
	if !safeID(e.EventID) || !safeID(e.BookID) || !safeID(e.CommandID) || !safeID(e.ActorID) || !safeID(e.CorrelationID) ||
		!safeID(e.CausationID) || e.Producer != "catalog-service" || e.SchemaVersion != "v1" ||
		e.IdempotencyKey != e.CommandID || e.LifecycleVersion < 1 || e.PayloadDigest == ([32]byte{}) ||
		e.OccurredAt.IsZero() {
		return ErrInvalidEvent
	}
	switch e.Kind {
	case LifecycleReindex:
		if e.SourceSHA256 == ([32]byte{}) || e.ManifestSHA256 == ([32]byte{}) ||
			!validArtifactReference(e.ManifestReference) || !strings.HasSuffix(e.ManifestReference, "/manifest.pb") {
			return ErrInvalidEvent
		}
	case LifecycleDelete:
		if e.SourceSHA256 != ([32]byte{}) || e.ManifestSHA256 != ([32]byte{}) || e.ManifestReference != "" {
			return ErrInvalidEvent
		}
	default:
		return ErrInvalidEvent
	}
	return nil
}

type DeletionCleanup struct {
	BookID, EventID, CommandID, CorrelationID string
	LifecycleVersion                          uint64
}

type LifecycleRepository interface {
	ApplyReindex(context.Context, LifecycleEvent, string, time.Time) (bool, error)
	FenceDeletion(context.Context, LifecycleEvent, time.Time) (bool, error)
	CompleteDeletion(context.Context, DeletionCleanup, time.Time) error
	PendingDeletionCleanup(context.Context, int, time.Time) ([]DeletionCleanup, error)
	RetryDeletionCleanup(context.Context, DeletionCleanup, time.Time) error
}

type LifecycleVectorIndex interface {
	DeleteBook(context.Context, string) error
}

type LifecycleCoordinator struct {
	repository LifecycleRepository
	vectors    LifecycleVectorIndex
	newID      func() (string, error)
	now        func() time.Time
}

func NewLifecycleCoordinator(repository LifecycleRepository, vectors LifecycleVectorIndex, newID func() (string, error), now func() time.Time) (*LifecycleCoordinator, error) {
	if repository == nil || vectors == nil || newID == nil || now == nil {
		return nil, errors.New("invalid lifecycle coordinator configuration")
	}
	return &LifecycleCoordinator{repository: repository, vectors: vectors, newID: newID, now: now}, nil
}

func (c *LifecycleCoordinator) HandleReindex(ctx context.Context, event LifecycleEvent) error {
	if event.Kind != LifecycleReindex {
		return ErrInvalidEvent
	}
	if err := event.Validate(); err != nil {
		return err
	}
	jobID, err := c.newID()
	if err != nil || !safeID(jobID) {
		return errors.New("generate indexing identity")
	}
	_, err = c.repository.ApplyReindex(ctx, event, jobID, c.now().UTC())
	return err
}

func (c *LifecycleCoordinator) HandleDeletion(ctx context.Context, event LifecycleEvent) error {
	if event.Kind != LifecycleDelete {
		return ErrInvalidEvent
	}
	if err := event.Validate(); err != nil {
		return err
	}
	cleanupRequired, err := c.repository.FenceDeletion(ctx, event, c.now().UTC())
	if err != nil || !cleanupRequired {
		return err
	}
	cleanup := DeletionCleanup{BookID: event.BookID, EventID: event.EventID, CommandID: event.CommandID, CorrelationID: event.CorrelationID, LifecycleVersion: event.LifecycleVersion}
	return c.completeDeletion(ctx, cleanup)
}

func (c *LifecycleCoordinator) RetryDeletions(ctx context.Context, limit int) error {
	cleanups, err := c.repository.PendingDeletionCleanup(ctx, limit, c.now().UTC())
	if err != nil {
		return err
	}
	for _, cleanup := range cleanups {
		if err = c.completeDeletion(ctx, cleanup); err != nil {
			if retryErr := c.repository.RetryDeletionCleanup(ctx, cleanup, c.now().UTC()); retryErr != nil {
				return errors.Join(err, retryErr)
			}
			return err
		}
	}
	return nil
}

func (c *LifecycleCoordinator) completeDeletion(ctx context.Context, cleanup DeletionCleanup) error {
	if err := c.vectors.DeleteBook(ctx, cleanup.BookID); err != nil {
		return errors.Join(errors.New("delete book vectors"), err)
	}
	if err := c.repository.CompleteDeletion(ctx, cleanup, c.now().UTC()); err != nil {
		return errors.Join(errors.New("complete book deletion"), err)
	}
	return nil
}
