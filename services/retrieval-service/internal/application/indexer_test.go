package application

import (
	"context"
	"crypto/sha256"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/domain"
)

func TestIndexerReplayUsesStableEvidenceIDsAndCompletesOnce(t *testing.T) {
	repository := &stubBatchRepository{metadata: BookProjection{BookID: "book-1", Title: "Systems", Author: "Author", Year: 2026}}
	reader := &stubShardReader{chunks: []Chunk{{ChunkID: "chunk-1", BookID: "book-1", Text: "Evidence", ContentSHA256: sha256.Sum256([]byte("Evidence")), PageStart: 1, PageEnd: 1}}}
	embedder := &stubDocumentEmbedder{vectors: [][]float32{make([]float32, 768)}}
	index := &stubVectorIndex{}
	indexer, err := NewIndexer(repository, reader, embedder, index, func() time.Time { return time.Date(2026, 7, 20, 10, 0, 0, 0, time.UTC) })
	if err != nil {
		t.Fatalf("NewIndexer() error = %v", err)
	}
	work := validBatchWork()
	repository.completeErr = errors.New("commit interrupted")
	if err = indexer.Process(context.Background(), work); err == nil {
		t.Fatal("first Process() error = nil")
	}
	repository.completeErr = nil
	if err = indexer.Process(context.Background(), work); err != nil {
		t.Fatalf("replayed Process() error = %v", err)
	}
	if len(index.chunkCalls) != 2 || index.chunkCalls[0][0].EvidenceID != index.chunkCalls[1][0].EvidenceID || repository.completed != 1 {
		t.Fatalf("replay was not stable: calls=%#v completed=%d", index.chunkCalls, repository.completed)
	}
}

func TestIndexerUpsertsDocumentCentroidBeforeActivation(t *testing.T) {
	first := make([]float32, domain.EmbeddingDimensions)
	first[0] = 1
	second := make([]float32, domain.EmbeddingDimensions)
	second[1] = 1
	repository := &stubBatchRepository{metadata: BookProjection{BookID: "book-1", Title: "Systems", Author: "Author", Year: 2026}}
	reader := &stubShardReader{chunks: []Chunk{
		{ChunkID: "chunk-1", BookID: "book-1", Text: "First", ContentSHA256: sha256.Sum256([]byte("First")), PageStart: 1, PageEnd: 1},
		{ChunkID: "chunk-2", BookID: "book-1", Text: "Second", ContentSHA256: sha256.Sum256([]byte("Second")), PageStart: 2, PageEnd: 2},
	}}
	embedder := &stubDocumentEmbedder{vectors: [][]float32{first, second}}
	index := &stubVectorIndex{}
	indexer, err := NewIndexer(repository, reader, embedder, index, func() time.Time { return time.Date(2026, 7, 20, 10, 0, 0, 0, time.UTC) })
	if err != nil {
		t.Fatalf("NewIndexer() error = %v", err)
	}
	work := validBatchWork()
	work.ChunkCount = 2

	if err = indexer.Process(context.Background(), work); err != nil {
		t.Fatalf("Process() error = %v", err)
	}

	if len(index.documentCalls) != 1 || len(index.activationCalls) != 1 {
		t.Fatalf("document upsert/activation calls = %d/%d", len(index.documentCalls), len(index.activationCalls))
	}
	document := index.documentCalls[0]
	if document.DocumentID != "book-1:job-1" || document.ChunkCount != 2 || document.PageStart != 1 || document.PageEnd != 2 {
		t.Fatalf("unexpected document: %#v", document)
	}
	if math.Abs(float64(document.Vector[0]-0.70710677)) > 0.0001 || math.Abs(float64(document.Vector[1]-0.70710677)) > 0.0001 {
		t.Fatalf("unexpected normalized centroid prefix: %v %v", document.Vector[0], document.Vector[1])
	}
}

func TestIndexerPreservesTerminalFailureCategories(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*stubBatchRepository, *stubShardReader, *stubDocumentEmbedder, *stubVectorIndex)
		want      domain.FailureCategory
	}{
		{
			name: "embedding outage",
			configure: func(_ *stubBatchRepository, _ *stubShardReader, embedder *stubDocumentEmbedder, _ *stubVectorIndex) {
				embedder.err = errors.New("tei unavailable")
			},
			want: domain.FailureEmbeddingUnavailable,
		},
		{
			name: "vector outage",
			configure: func(_ *stubBatchRepository, _ *stubShardReader, _ *stubDocumentEmbedder, index *stubVectorIndex) {
				index.upsertChunksErr = errors.New("qdrant unavailable")
			},
			want: domain.FailureVectorStoreUnavailable,
		},
		{
			name: "resource limit",
			configure: func(_ *stubBatchRepository, reader *stubShardReader, _ *stubDocumentEmbedder, _ *stubVectorIndex) {
				reader.chunks[0].Text = string(make([]byte, (32<<10)+1))
			},
			want: domain.FailureResourceLimit,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &stubBatchRepository{metadata: BookProjection{BookID: "book-1", Title: "Systems", Author: "Author", Year: 2026}}
			reader := &stubShardReader{chunks: []Chunk{{ChunkID: "chunk-1", BookID: "book-1", Text: "Evidence", ContentSHA256: sha256.Sum256([]byte("Evidence")), PageStart: 1, PageEnd: 1}}}
			embedder := &stubDocumentEmbedder{vectors: [][]float32{make([]float32, domain.EmbeddingDimensions)}}
			index := &stubVectorIndex{}
			test.configure(repository, reader, embedder, index)
			indexer, err := NewIndexer(repository, reader, embedder, index, func() time.Time { return time.Date(2026, 7, 20, 10, 0, 0, 0, time.UTC) })
			if err != nil {
				t.Fatalf("NewIndexer() error = %v", err)
			}

			err = indexer.Process(context.Background(), validBatchWork())

			if got := FailureCategory(err); got != test.want {
				t.Fatalf("FailureCategory() = %s, want %s, err=%v", got, test.want, err)
			}
		})
	}
}

func validBatchWork() BatchWork {
	profile := domain.SupportedIndexProfile().Digest
	return BatchWork{EventID: "batch-event-1", JobID: "job-1", BatchID: "job-1:0", BookID: "book-1", ShardReference: "books/book-1/profile/shards/000000.pb.zst",
		ShardSHA256: sum(5), CompressedBytes: 10, UncompressedBytes: 20, ChunkCount: 1, SourceSHA256: sum(1), ManifestSHA256: sum(2),
		ProfileDigest: profile, CorrelationID: "correlation-1", CausationID: "manifest-1", Producer: "retrieval-service", SchemaVersion: "v1",
		IdempotencyKey: "job-1:0", OccurredAt: time.Date(2026, 7, 20, 9, 0, 0, 0, time.UTC)}
}

type stubBatchRepository struct {
	metadata    BookProjection
	completeErr error
	completed   int
}

func (s *stubBatchRepository) BeginBatch(context.Context, BatchWork) (BookProjection, bool, error) {
	return s.metadata, true, nil
}
func (s *stubBatchRepository) CompleteBatch(_ context.Context, _ BatchWork, _ []EvidenceRecord, _ time.Time) (bool, error) {
	if s.completeErr != nil {
		return false, s.completeErr
	}
	s.completed++
	return true, nil
}
func (s *stubBatchRepository) DocumentRecord(_ context.Context, work BatchWork) (DocumentRecord, error) {
	return DocumentRecord{DocumentResult: DocumentResult{DocumentID: work.BookID + ":" + work.JobID, JobID: work.JobID, BookID: work.BookID, Title: s.metadata.Title,
		Author: s.metadata.Author, Year: s.metadata.Year, Tags: s.metadata.Tags, ChunkCount: work.ChunkCount, PageStart: 1, PageEnd: work.ChunkCount},
		Vector: mustCentroid(int(work.ChunkCount))}, nil
}
func (s *stubBatchRepository) FinalizeJob(context.Context, BatchWork, time.Time) error { return nil }

type stubShardReader struct {
	chunks []Chunk
	err    error
}

func (s *stubShardReader) ReadShard(context.Context, BatchWork) ([]Chunk, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.chunks, nil
}

type stubDocumentEmbedder struct {
	vectors [][]float32
	err     error
}

func (s *stubDocumentEmbedder) EmbedDocuments(context.Context, []string) ([][]float32, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.vectors, nil
}

type stubVectorIndex struct {
	chunkCalls      [][]EvidenceRecord
	documentCalls   []DocumentRecord
	activationCalls []string
	upsertChunksErr error
}

func (s *stubVectorIndex) UpsertChunks(_ context.Context, values []EvidenceRecord) error {
	if s.upsertChunksErr != nil {
		return s.upsertChunksErr
	}
	copyValues := append([]EvidenceRecord(nil), values...)
	s.chunkCalls = append(s.chunkCalls, copyValues)
	return nil
}
func (s *stubVectorIndex) UpsertDocument(_ context.Context, value DocumentRecord) error {
	s.documentCalls = append(s.documentCalls, value)
	return nil
}
func (s *stubVectorIndex) ActivateJob(_ context.Context, jobID string) error {
	s.activationCalls = append(s.activationCalls, jobID)
	return nil
}
func (s *stubVectorIndex) DeactivateJob(context.Context, string) error { return nil }

func mustCentroid(chunkCount int) []float32 {
	vectors := make([][]float32, chunkCount)
	for index := range vectors {
		vectors[index] = make([]float32, domain.EmbeddingDimensions)
		vectors[index][index] = 1
	}
	vector, err := NormalizedCentroid(vectors)
	if err != nil {
		panic(err)
	}
	return vector
}
