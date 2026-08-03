package artifact

import (
	"bytes"
	"context"
	"testing"
	"time"

	ingestionv1 "github.com/belLena81/raglibrarian/pkg/proto/ingestion/v1"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/domain"
	"google.golang.org/protobuf/proto"
)

type memoryStore struct{ values map[string][]byte }

func (s *memoryStore) Put(_ context.Context, reference string, contents []byte, _ [32]byte) error {
	if s.values == nil {
		s.values = make(map[string][]byte)
	}
	if existing, ok := s.values[reference]; ok && !bytes.Equal(existing, contents) {
		return ErrArtifactConflict
	}
	s.values[reference] = append([]byte(nil), contents...)
	return nil
}

func (s *memoryStore) Delete(_ context.Context, reference string) error {
	delete(s.values, reference)
	return nil
}

func TestWriterCommitsManifestLast(t *testing.T) {
	store := &memoryStore{}
	writer, err := NewWriter(store, Metadata{BookID: "book-1", SourceSHA256: sum(1), ConfigDigest: sum(2), GeneratedAt: time.Now().UTC(), LifecycleVersion: 1}, Versions{Extraction: "e1", Normalization: "n1", Tokenizer: "t1", Chunking: "c1", Structure: "s1"}, ProcessingProfile{MaximumTokens: 800, OverlapTokens: 120}, Limits{ChunksPerShard: 2, MaximumShardBytes: 1 << 20, MaximumManifestBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	for index := range 3 {
		chunk, _ := domain.NewChunk(domain.ChunkInput{ID: string(rune('a' + index)), BookID: "book-1", Order: uint64(index), Text: "safe synthetic text", PageStart: 1, PageEnd: 1, TokenStart: uint64(index), TokenEnd: uint64(index + 1)})
		if err = writer.Add(context.Background(), chunk); err != nil {
			t.Fatal(err)
		}
	}
	result, err := writer.Finalize(context.Background(), 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.ChunkCount != 3 || len(store.values) != 3 {
		t.Fatalf("unexpected artifacts: %#v count=%d", result, len(store.values))
	}
	if _, ok := store.values[result.ManifestReference]; !ok {
		t.Fatal("manifest was not committed")
	}
}

func TestWriterAbortRemovesUncommittedShards(t *testing.T) {
	store := &memoryStore{}
	writer, err := NewWriter(store, Metadata{BookID: "book-1", SourceSHA256: sum(1), ConfigDigest: sum(2), GeneratedAt: time.Now().UTC(), LifecycleVersion: 1}, Versions{Extraction: "e1", Normalization: "n1", Tokenizer: "t1", Chunking: "c1", Structure: "s1"}, ProcessingProfile{MaximumTokens: 800, OverlapTokens: 120}, Limits{ChunksPerShard: 1, MaximumShardBytes: 1 << 20, MaximumManifestBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	for index := range 2 {
		chunk, _ := domain.NewChunk(domain.ChunkInput{ID: string(rune('a' + index)), BookID: "book-1", Order: uint64(index), Text: "safe synthetic text", PageStart: 1, PageEnd: 1, TokenStart: uint64(index), TokenEnd: uint64(index + 1)})
		if err = writer.Add(context.Background(), chunk); err != nil {
			t.Fatal(err)
		}
	}
	if len(store.values) != 1 {
		t.Fatalf("expected one flushed shard, got %d", len(store.values))
	}
	if err = writer.Abort(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.values) != 0 {
		t.Fatalf("expected cleanup, got %d artifacts", len(store.values))
	}
}

func TestWriterRetryProducesByteIdenticalArtifacts(t *testing.T) {
	store := &memoryStore{}
	generatedAt := time.Date(2026, 7, 19, 10, 0, 0, 0, time.UTC)
	write := func() Result {
		writer, err := NewWriter(store, Metadata{BookID: "book-1", SourceSHA256: sum(1), ConfigDigest: sum(2), GeneratedAt: generatedAt, LifecycleVersion: 1}, Versions{Extraction: "e1", Normalization: "n1", Tokenizer: "t1", Chunking: "c1", Structure: "s1"}, ProcessingProfile{MaximumTokens: 800, OverlapTokens: 120}, Limits{ChunksPerShard: 2, MaximumShardBytes: 1 << 20, MaximumManifestBytes: 1 << 20})
		if err != nil {
			t.Fatal(err)
		}
		chunk, err := domain.NewChunk(domain.ChunkInput{ID: "chunk-1", BookID: "book-1", Text: "safe synthetic text", PageStart: 1, PageEnd: 1, TokenStart: 0, TokenEnd: 3})
		if err != nil {
			t.Fatal(err)
		}
		if err = writer.Add(context.Background(), chunk); err != nil {
			t.Fatal(err)
		}
		result, err := writer.Finalize(context.Background(), 1, nil)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	first := write()
	second := write()
	if first.ManifestSHA256 != second.ManifestSHA256 || first.ManifestReference != second.ManifestReference {
		t.Fatalf("retry changed deterministic manifest identity")
	}
}

func TestWriterRecordsTextFreeContentSelectionAudit(t *testing.T) {
	store := &memoryStore{}
	writer, err := NewWriter(store, Metadata{BookID: "book-1", SourceSHA256: sum(1), ConfigDigest: sum(2), GeneratedAt: time.Now().UTC(), LifecycleVersion: 1}, Versions{Extraction: "e1", Normalization: "n1", Tokenizer: "t1", Chunking: "c1", Structure: "s1"}, ProcessingProfile{MaximumTokens: 800, OverlapTokens: 120}, Limits{ChunksPerShard: 2, MaximumShardBytes: 1 << 20, MaximumManifestBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	chunk, err := domain.NewChunk(domain.ChunkInput{ID: "chunk-1", BookID: "book-1", Text: "retained synthetic text", PageStart: 2, PageEnd: 2, TokenEnd: 3})
	if err != nil {
		t.Fatal(err)
	}
	if err = writer.Add(context.Background(), chunk); err != nil {
		t.Fatal(err)
	}
	audit := &ContentSelection{
		Mode: "enforcement", SelectorVersion: "layout-selector-v1", ParserVersion: "docling-serve-v1.21.0",
		ModelDigest: sum(3), PolicyDigest: sum(4), ProcessingProfileDigest: sum(2), FallbackReason: "none",
		OriginalLocationCount: 4, Ranges: []ContentSelectionRange{{Start: 1, End: 1, Reason: "title"}},
	}
	result, err := writer.Finalize(context.Background(), 4, audit)
	if err != nil {
		t.Fatal(err)
	}
	var manifest ingestionv1.ChunkManifestV1
	if err = proto.Unmarshal(store.values[result.ManifestReference], &manifest); err != nil {
		t.Fatal(err)
	}
	selection := manifest.GetContentSelection()
	if selection == nil || selection.OriginalOrdinalCount != 4 || selection.ExcludedOrdinalCount != 1 || selection.RetainedOrdinalCount != 3 || selection.ExcludedRatio != 0.25 || len(selection.ExcludedRanges) != 1 {
		t.Fatalf("selection audit = %#v", selection)
	}
	if selection.ExcludedRanges[0].Reason != ingestionv1.ContentExclusionReason_CONTENT_EXCLUSION_REASON_TITLE_PAGE {
		t.Fatalf("selection reason = %v", selection.ExcludedRanges[0].Reason)
	}
}

func TestWriterRejectsMalformedContentSelectionAudit(t *testing.T) {
	store := &memoryStore{}
	writer, err := NewWriter(store, Metadata{BookID: "book-1", SourceSHA256: sum(1), ConfigDigest: sum(2), GeneratedAt: time.Now().UTC(), LifecycleVersion: 1}, Versions{Extraction: "e1", Normalization: "n1", Tokenizer: "t1", Chunking: "c1", Structure: "s1"}, ProcessingProfile{MaximumTokens: 800, OverlapTokens: 120}, Limits{ChunksPerShard: 2, MaximumShardBytes: 1 << 20, MaximumManifestBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	chunk, _ := domain.NewChunk(domain.ChunkInput{ID: "chunk-1", BookID: "book-1", Text: "retained", PageStart: 1, PageEnd: 1, TokenEnd: 1})
	if err = writer.Add(context.Background(), chunk); err != nil {
		t.Fatal(err)
	}
	_, err = writer.Finalize(context.Background(), 1, &ContentSelection{Mode: "enforcement", OriginalLocationCount: 1, Ranges: []ContentSelectionRange{{Start: 2, End: 2, Reason: "title"}}})
	if err == nil {
		t.Fatal("malformed selection audit was accepted")
	}
}

func TestWriterRequiresContentSelectionAuditWhenConfigured(t *testing.T) {
	store := &memoryStore{}
	writer, err := NewWriter(
		store,
		Metadata{BookID: "book-1", SourceSHA256: sum(1), ConfigDigest: sum(2), GeneratedAt: time.Now().UTC(), LifecycleVersion: 1},
		Versions{Extraction: "e1", Normalization: "n1", Tokenizer: "t1", Chunking: "c1", Structure: "s1"},
		ProcessingProfile{MaximumTokens: 800, OverlapTokens: 120, RequireContentSelection: true},
		Limits{ChunksPerShard: 2, MaximumShardBytes: 1 << 20, MaximumManifestBytes: 1 << 20},
	)
	if err != nil {
		t.Fatal(err)
	}
	chunk, err := domain.NewChunk(domain.ChunkInput{
		ID: "chunk-1", BookID: "book-1", Text: "retained",
		PageStart: 1, PageEnd: 1, TokenEnd: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = writer.Add(context.Background(), chunk); err != nil {
		t.Fatal(err)
	}
	if _, err = writer.Finalize(context.Background(), 1, nil); err == nil {
		t.Fatal("missing content selection audit was accepted")
	}
}

func TestWriterResolvesFailOpenOrdinalCount(t *testing.T) {
	store := &memoryStore{}
	writer, err := NewWriter(store, Metadata{BookID: "book-1", SourceSHA256: sum(1), ConfigDigest: sum(2), GeneratedAt: time.Now().UTC(), LifecycleVersion: 1}, Versions{Extraction: "e1", Normalization: "n1", Tokenizer: "t1", Chunking: "c1", Structure: "s1"}, ProcessingProfile{MaximumTokens: 800, OverlapTokens: 120}, Limits{ChunksPerShard: 2, MaximumShardBytes: 1 << 20, MaximumManifestBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	chunk, _ := domain.NewChunk(domain.ChunkInput{ID: "chunk-1", BookID: "book-1", Text: "retained", PageStart: 1, PageEnd: 1, TokenEnd: 1})
	if err = writer.Add(context.Background(), chunk); err != nil {
		t.Fatal(err)
	}
	audit := &ContentSelection{Mode: "enforcement", SelectorVersion: "layout-selector-v1", ParserVersion: "docling-serve-v1.21.0", ModelDigest: sum(3), PolicyDigest: sum(4), ProcessingProfileDigest: sum(2), FallbackReason: "invalid_output"}
	result, err := writer.Finalize(context.Background(), 2, audit)
	if err != nil {
		t.Fatal(err)
	}
	var manifest ingestionv1.ChunkManifestV1
	if err = proto.Unmarshal(store.values[result.ManifestReference], &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.GetContentSelection().GetOriginalOrdinalCount() != 2 || manifest.GetContentSelection().GetRetainedOrdinalCount() != 2 {
		t.Fatalf("fail-open selection = %#v", manifest.GetContentSelection())
	}
}

func sum(value byte) [32]byte {
	var result [32]byte
	result[0] = value
	return result
}
