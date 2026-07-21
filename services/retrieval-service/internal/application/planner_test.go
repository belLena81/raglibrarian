package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/domain"
)

func TestPlannerConvergesForEitherEventOrderAndDuplicates(t *testing.T) {
	orders := [][]string{{"metadata", "manifest"}, {"manifest", "metadata"}}
	for _, order := range orders {
		t.Run(order[0]+"_first", func(t *testing.T) {
			repository := newMemoryPlanningRepository()
			planner := newTestPlanner(t, repository)
			for _, fact := range order {
				if err := handleFact(planner, fact); err != nil {
					t.Fatalf("handle %s: %v", fact, err)
				}
			}
			for _, fact := range order {
				if err := handleFact(planner, fact); err != nil {
					t.Fatalf("duplicate %s: %v", fact, err)
				}
			}
			if repository.planCount != 1 || len(repository.batches) != 2 {
				t.Fatalf("plans = %d, batches = %d; want one plan and two batches", repository.planCount, len(repository.batches))
			}
		})
	}
}

func TestPlannerRejectsConflictingDuplicateFact(t *testing.T) {
	repository := newMemoryPlanningRepository()
	planner := newTestPlanner(t, repository)
	if err := planner.HandleMetadata(context.Background(), validMetadataEvent()); err != nil {
		t.Fatalf("HandleMetadata() error = %v", err)
	}
	conflict := validMetadataEvent()
	conflict.Title = "changed"
	conflict.PayloadDigest[0]++
	if err := planner.HandleMetadata(context.Background(), conflict); !errors.Is(err, ErrConflictingEvent) {
		t.Fatalf("conflicting HandleMetadata() error = %v", err)
	}
}

func TestPlannerRejectsIncompatibleManifestProfile(t *testing.T) {
	repository := newMemoryPlanningRepository()
	planner := newTestPlanner(t, repository)
	event := validManifestEvent()
	event.Manifest.MaximumTokens = 801
	if err := planner.HandleManifest(context.Background(), event); !errors.Is(err, ErrUnsupportedIndexProfile) {
		t.Fatalf("HandleManifest() error = %v", err)
	}
}

func TestPlannerRejectsManifestPageCountAboveSharedLimit(t *testing.T) {
	repository := newMemoryPlanningRepository()
	planner := newTestPlanner(t, repository)
	event := validManifestEvent()
	event.Manifest.PageCount = 501
	if err := planner.HandleManifest(context.Background(), event); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("HandleManifest() error = %v", err)
	}
}

func TestManifestFailureCategoryRequiresValidatedEnvelope(t *testing.T) {
	event := validManifestEvent()
	event.Manifest = Manifest{}
	category, terminal := ManifestFailureCategory(event, ErrInvalidEvent)
	if !terminal || category != domain.FailureManifestIntegrity {
		t.Fatalf("ManifestFailureCategory() = %q, %v", category, terminal)
	}
	event.BookID = "invalid/book"
	if _, terminal = ManifestFailureCategory(event, ErrInvalidEvent); terminal {
		t.Fatal("invalid outer event was classified as a terminal manifest failure")
	}
}

func TestPlannerRejectsArtifactSubstitutionAndResourceBombs(t *testing.T) {
	for name, mutate := range map[string]func(*ManifestEvent){
		"manifest path": func(event *ManifestEvent) { event.ManifestReference = "books/other/manifest.pb" },
		"shard path": func(event *ManifestEvent) {
			event.Manifest.Shards[0].Reference = "books/book-1/other/shards/000000.pb.zst"
		},
		"uncompressed bomb": func(event *ManifestEvent) { event.Manifest.Shards[0].UncompressedBytes = 64<<20 + 1 },
	} {
		t.Run(name, func(t *testing.T) {
			event := validManifestEvent()
			mutate(&event)
			if err := event.Validate(domain.SupportedIndexProfile()); !errors.Is(err, ErrInvalidEvent) {
				t.Fatalf("Validate() error = %v", err)
			}
		})
	}
}

func newTestPlanner(t *testing.T, repository PlanningRepository) *Planner {
	t.Helper()
	planner, err := NewPlanner(repository, func() (string, error) { return "job-1", nil }, func() time.Time {
		return time.Date(2026, 7, 20, 10, 0, 0, 0, time.UTC)
	})
	if err != nil {
		t.Fatalf("NewPlanner() error = %v", err)
	}
	return planner
}

func handleFact(planner *Planner, fact string) error {
	if fact == "metadata" {
		return planner.HandleMetadata(context.Background(), validMetadataEvent())
	}
	return planner.HandleManifest(context.Background(), validManifestEvent())
}

func validMetadataEvent() MetadataEvent {
	return MetadataEvent{EventID: "event-metadata", BookID: "book-1", Title: "Systems", Author: "A. Author", Year: 2026,
		Tags: []string{"distributed"}, SourceSHA256: sum(1), CorrelationID: "correlation-1", CausationID: "upload-1",
		Producer: "catalog-service", SchemaVersion: "v1", IdempotencyKey: "book-1", OccurredAt: time.Date(2026, 7, 20, 9, 0, 0, 0, time.UTC), PayloadDigest: sum(3)}
}

func validManifestEvent() ManifestEvent {
	processingDigest := sum(7)
	processingDigestHex := "0700000000000000000000000000000000000000000000000000000000000000"
	prefix := "books/book-1/0100000000000000000000000000000000000000000000000000000000000000/" + processingDigestHex + "/"
	return ManifestEvent{EventID: "event-manifest", BookID: "book-1", SourceSHA256: sum(1), ManifestSHA256: sum(2),
		ManifestReference: prefix + "manifest.pb", CorrelationID: "correlation-1", CausationID: "processing-1",
		Producer: "ingestion-service", SchemaVersion: "v1", IdempotencyKey: "book-1:" + processingDigestHex + ":ready", OccurredAt: time.Date(2026, 7, 20, 9, 5, 0, 0, time.UTC), PayloadDigest: sum(4),
		Manifest: Manifest{SchemaVersion: "v1", BookID: "book-1", SourceSHA256: sum(1), ManifestSHA256: sum(2),
			ProcessingConfigDigest: processingDigest, PageCount: 2, ChunkCount: 3, GeneratedAt: time.Date(2026, 7, 20, 9, 4, 0, 0, time.UTC),
			ExtractionVersion: "poppler-layout-v1", NormalizationVersion: "nfc-v1", TokenizerVersion: "cl100k_base-v1",
			ChunkingVersion: "token-window-v2", StructureVersion: "heading-carry-v1", MaximumTokens: 800, OverlapTokens: 120,
			Shards: []Shard{{Reference: prefix + "shards/000000.pb.zst", SHA256: sum(5), CompressedBytes: 10, UncompressedBytes: 20, ChunkCount: 2, FirstChunkOrder: 0, LastChunkOrder: 1},
				{Reference: prefix + "shards/000001.pb.zst", SHA256: sum(6), CompressedBytes: 11, UncompressedBytes: 21, ChunkCount: 1, FirstChunkOrder: 2, LastChunkOrder: 2}}}}
}

func sum(value byte) [32]byte { var result [32]byte; result[0] = value; return result }

type memoryPlanningRepository struct {
	metadata  *MetadataEvent
	manifest  *ManifestEvent
	planned   bool
	planCount int
	batches   []BatchPlan
}

func newMemoryPlanningRepository() *memoryPlanningRepository { return &memoryPlanningRepository{} }

func (r *memoryPlanningRepository) ProjectMetadata(_ context.Context, event MetadataEvent) (PlanningSnapshot, error) {
	if r.metadata != nil && r.metadata.PayloadDigest != event.PayloadDigest {
		return PlanningSnapshot{}, ErrConflictingEvent
	}
	copy := event
	r.metadata = &copy
	return r.snapshot(), nil
}

func (r *memoryPlanningRepository) ProjectManifest(_ context.Context, event ManifestEvent) (PlanningSnapshot, error) {
	if r.manifest != nil && r.manifest.PayloadDigest != event.PayloadDigest {
		return PlanningSnapshot{}, ErrConflictingEvent
	}
	copy := event
	r.manifest = &copy
	return r.snapshot(), nil
}

func (r *memoryPlanningRepository) CommitPlan(_ context.Context, _ PlanningSnapshot, batches []BatchPlan) (bool, error) {
	if r.planned {
		return false, nil
	}
	r.planned = true
	r.planCount++
	r.batches = append([]BatchPlan(nil), batches...)
	return true, nil
}

func (r *memoryPlanningRepository) snapshot() PlanningSnapshot {
	return PlanningSnapshot{Metadata: r.metadata, Manifest: r.manifest, Planned: r.planned}
}
