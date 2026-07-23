package application

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestLifecycleDeletionFencesBeforeVectorCleanup(t *testing.T) {
	steps := make([]string, 0, 3)
	repository := &lifecycleRepositoryStub{steps: &steps, cleanupRequired: true}
	vectors := &lifecycleVectorStub{steps: &steps}
	coordinator, err := NewLifecycleCoordinator(repository, vectors, func() (string, error) {
		return "job-new", nil
	}, func() time.Time { return lifecycleNow() })
	if err != nil {
		t.Fatal(err)
	}

	if err = coordinator.HandleDeletion(context.Background(), validLifecycleEvent(LifecycleDelete)); err != nil {
		t.Fatal(err)
	}
	want := []string{"fence", "delete-vectors", "complete"}
	if len(steps) != len(want) {
		t.Fatalf("steps = %#v", steps)
	}
	for index := range want {
		if steps[index] != want[index] {
			t.Fatalf("steps = %#v, want %#v", steps, want)
		}
	}
}

func TestLifecycleDeletionRemainsPendingWhenVectorCleanupFails(t *testing.T) {
	steps := make([]string, 0, 2)
	repository := &lifecycleRepositoryStub{steps: &steps, cleanupRequired: true}
	vectors := &lifecycleVectorStub{steps: &steps, err: errors.New("unavailable")}
	coordinator, _ := NewLifecycleCoordinator(repository, vectors, func() (string, error) {
		return "job-new", nil
	}, lifecycleNow)

	if err := coordinator.HandleDeletion(context.Background(), validLifecycleEvent(LifecycleDelete)); err == nil {
		t.Fatal("HandleDeletion() succeeded while vector cleanup failed")
	}
	if len(steps) != 2 || steps[0] != "fence" || steps[1] != "delete-vectors" {
		t.Fatalf("steps = %#v", steps)
	}
}

func TestLifecycleReindexDelegatesValidatedGeneration(t *testing.T) {
	repository := &lifecycleRepositoryStub{}
	coordinator, _ := NewLifecycleCoordinator(repository, &lifecycleVectorStub{}, func() (string, error) {
		return "job-new", nil
	}, lifecycleNow)
	event := validLifecycleEvent(LifecycleReindex)

	if err := coordinator.HandleReindex(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if repository.reindexJobID != "job-new" || repository.reindexVersion != event.LifecycleVersion {
		t.Fatalf("reindex job=%q version=%d", repository.reindexJobID, repository.reindexVersion)
	}
}

func TestDeletionCrossingVectorUpsertRequiresSecondCleanupBeforeAck(t *testing.T) {
	lifecycleRepository := &lifecycleRepositoryStub{cleanupRequired: true, processing: true}
	lifecycleVectors := &lifecycleVectorStub{}
	coordinator, _ := NewLifecycleCoordinator(lifecycleRepository, lifecycleVectors, func() (string, error) {
		return "job-new", nil
	}, lifecycleNow)
	batchRepository := &stubBatchRepository{
		metadata: BookProjection{BookID: "book-1", Title: "Systems", Author: "Author", Year: 2026},
		complete: func() (bool, error) {
			lifecycleRepository.processing = false
			return false, ErrConflictingEvent
		},
	}
	index := &stubVectorIndex{upsertChunks: func() error {
		err := coordinator.HandleDeletion(context.Background(), validLifecycleEvent(LifecycleDelete))
		if !errors.Is(err, ErrLifecycleCleanupPending) {
			t.Fatalf("HandleDeletion() error = %v, want cleanup pending", err)
		}
		if lifecycleRepository.acknowledged != 0 {
			t.Fatal("deletion was acknowledged while a vector writer was active")
		}
		return nil
	}}
	indexer, _ := NewIndexer(
		batchRepository,
		&stubShardReader{chunks: []Chunk{validChunk("Evidence")}},
		&stubDocumentEmbedder{vectors: [][]float32{make([]float32, 768)}},
		index,
		lifecycleNow,
	)

	err := indexer.Process(context.Background(), validBatchWork())
	if !errors.Is(err, ErrConflictingEvent) {
		t.Fatalf("Process() error = %v, want lifecycle conflict", err)
	}
	if err = coordinator.RetryDeletions(context.Background(), 1); err != nil {
		t.Fatalf("RetryDeletions() error = %v", err)
	}
	if lifecycleVectors.deletions != 2 || lifecycleRepository.acknowledged != 1 {
		t.Fatalf("vector deletions=%d acknowledgements=%d, want 2/1", lifecycleVectors.deletions, lifecycleRepository.acknowledged)
	}
}

func TestDeletionCrossingLastBatchFinalizationRequiresSecondCleanupBeforeAck(t *testing.T) {
	lifecycleRepository := &lifecycleRepositoryStub{cleanupRequired: true}
	lifecycleVectors := &lifecycleVectorStub{}
	coordinator, _ := NewLifecycleCoordinator(lifecycleRepository, lifecycleVectors, func() (string, error) {
		return "job-new", nil
	}, lifecycleNow)
	batchRepository := &stubBatchRepository{
		metadata: BookProjection{BookID: "book-1", Title: "Systems", Author: "Author", Year: 2026},
		complete: func() (bool, error) {
			lifecycleRepository.finalizing = true
			return true, nil
		},
		finalize: func() error {
			lifecycleRepository.finalizing = false
			return ErrConflictingEvent
		},
	}
	index := &stubVectorIndex{upsertDocument: func() error {
		err := coordinator.HandleDeletion(context.Background(), validLifecycleEvent(LifecycleDelete))
		if !errors.Is(err, ErrLifecycleCleanupPending) {
			t.Fatalf("HandleDeletion() error = %v, want finalization pending", err)
		}
		if lifecycleRepository.acknowledged != 0 {
			t.Fatal("deletion was acknowledged before document upsert and activation quiesced")
		}
		return nil
	}}
	indexer, _ := NewIndexer(
		batchRepository,
		&stubShardReader{chunks: []Chunk{validChunk("Evidence")}},
		&stubDocumentEmbedder{vectors: [][]float32{make([]float32, 768)}},
		index,
		lifecycleNow,
	)

	if err := indexer.Process(context.Background(), validBatchWork()); err == nil {
		t.Fatal("Process() succeeded after deletion fenced finalization")
	}
	if len(index.documentCalls) != 1 || len(index.activationCalls) != 1 || lifecycleRepository.acknowledged != 0 {
		t.Fatalf("document calls=%d activation calls=%d acknowledgements=%d", len(index.documentCalls), len(index.activationCalls), lifecycleRepository.acknowledged)
	}
	if err := coordinator.RetryDeletions(context.Background(), 1); err != nil {
		t.Fatalf("RetryDeletions() error = %v", err)
	}
	if lifecycleVectors.deletions != 2 || lifecycleRepository.acknowledged != 1 {
		t.Fatalf("vector deletions=%d acknowledgements=%d, want 2/1", lifecycleVectors.deletions, lifecycleRepository.acknowledged)
	}
}

func validLifecycleEvent(kind LifecycleKind) LifecycleEvent {
	event := LifecycleEvent{EventID: "event-2", BookID: "book-1", CommandID: "command-2", ActorID: "actor-1",
		CorrelationID: "correlation-2", CausationID: "command-2", Producer: "catalog-service", SchemaVersion: "v1",
		IdempotencyKey: "command-2", Kind: kind, LifecycleVersion: 2, PayloadDigest: sum(9), OccurredAt: lifecycleNow()}
	if kind == LifecycleReindex {
		event.SourceSHA256 = sum(1)
		event.ManifestSHA256 = sum(2)
		event.ManifestReference = "books/book-1/source/profile/manifest.pb"
	}
	return event
}

func lifecycleNow() time.Time { return time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC) }

type lifecycleRepositoryStub struct {
	steps           *[]string
	cleanupRequired bool
	reindexJobID    string
	reindexVersion  uint64
	processing      bool
	finalizing      bool
	acknowledged    int
	cleanup         DeletionCleanup
}

func (r *lifecycleRepositoryStub) ApplyReindex(_ context.Context, event LifecycleEvent, jobID string, _ time.Time) (bool, error) {
	r.reindexJobID = jobID
	r.reindexVersion = event.LifecycleVersion
	return true, nil
}
func (r *lifecycleRepositoryStub) FenceDeletion(_ context.Context, event LifecycleEvent, _ time.Time) (bool, error) {
	if r.steps != nil {
		*r.steps = append(*r.steps, "fence")
	}
	r.cleanup = DeletionCleanup{BookID: event.BookID, EventID: event.EventID, CommandID: event.CommandID, CorrelationID: event.CorrelationID, LifecycleVersion: event.LifecycleVersion}
	return r.cleanupRequired, nil
}
func (r *lifecycleRepositoryStub) CompleteDeletion(context.Context, DeletionCleanup, time.Time) error {
	if r.processing || r.finalizing {
		return ErrLifecycleCleanupPending
	}
	if r.steps != nil {
		*r.steps = append(*r.steps, "complete")
	}
	r.acknowledged++
	r.cleanupRequired = false
	return nil
}
func (r *lifecycleRepositoryStub) PendingDeletionCleanup(context.Context, int, time.Time) ([]DeletionCleanup, error) {
	if r.cleanupRequired {
		return []DeletionCleanup{r.cleanup}, nil
	}
	return nil, nil
}
func (r *lifecycleRepositoryStub) RetryDeletionCleanup(context.Context, DeletionCleanup, time.Time) error {
	return nil
}

type lifecycleVectorStub struct {
	steps     *[]string
	err       error
	deletions int
}

func (v *lifecycleVectorStub) DeleteBook(context.Context, string) error {
	v.deletions++
	if v.steps != nil {
		*v.steps = append(*v.steps, "delete-vectors")
	}
	return v.err
}
