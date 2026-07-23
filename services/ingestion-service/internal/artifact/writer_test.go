package artifact

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/domain"
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
	result, err := writer.Finalize(context.Background(), 1)
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
		result, err := writer.Finalize(context.Background(), 1)
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

func sum(value byte) [32]byte {
	var result [32]byte
	result[0] = value
	return result
}
