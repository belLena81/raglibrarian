package application

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/domain"
)

func TestIndexerReplayUsesStableEvidenceIDsAndCompletesOnce(t *testing.T) {
	repository := &stubBatchRepository{metadata: BookProjection{BookID: "book-1", Title: "Systems", Author: "Author", Year: 2026}}
	reader := &stubShardReader{chunks: []Chunk{validChunk("Evidence")}}
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
		validChunk("First"),
		validChunkWithPosition("Second", 1, 2, 2, 1, 3),
	}}
	embedder := &stubDocumentEmbedder{vectors: [][]float32{first, second}}
	index := &stubVectorIndex{}
	indexer, err := NewIndexer(repository, reader, embedder, index, func() time.Time { return time.Date(2026, 7, 20, 10, 0, 0, 0, time.UTC) })
	if err != nil {
		t.Fatalf("NewIndexer() error = %v", err)
	}
	work := validBatchWork()
	work.ChunkCount = 2
	work.LastChunkOrder = 1

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
				text := string(make([]byte, (32<<10)+1))
				reader.chunks[0].Text = text
				reader.chunks[0].ContentSHA256 = sha256.Sum256([]byte(text))
			},
			want: domain.FailureResourceLimit,
		},
		{
			name: "wrong book id",
			configure: func(_ *stubBatchRepository, reader *stubShardReader, _ *stubDocumentEmbedder, _ *stubVectorIndex) {
				reader.chunks[0].BookID = "book-2"
			},
			want: domain.FailureManifestIntegrity,
		},
		{
			name: "content digest mismatch",
			configure: func(_ *stubBatchRepository, reader *stubShardReader, _ *stubDocumentEmbedder, _ *stubVectorIndex) {
				reader.chunks[0].ContentSHA256 = sum(99)
			},
			want: domain.FailureManifestIntegrity,
		},
		{
			name: "invalid page range",
			configure: func(_ *stubBatchRepository, reader *stubShardReader, _ *stubDocumentEmbedder, _ *stubVectorIndex) {
				reader.chunks[0].PageStart = 2
				reader.chunks[0].PageEnd = 1
			},
			want: domain.FailureManifestIntegrity,
		},
		{
			name: "page exceeds manifest count",
			configure: func(_ *stubBatchRepository, reader *stubShardReader, _ *stubDocumentEmbedder, _ *stubVectorIndex) {
				reader.chunks[0].PageEnd = 3
			},
			want: domain.FailureManifestIntegrity,
		},
		{
			name: "token width exceeds profile",
			configure: func(_ *stubBatchRepository, reader *stubShardReader, _ *stubDocumentEmbedder, _ *stubVectorIndex) {
				reader.chunks[0].TokenEnd = uint64(domain.SupportedIndexProfile().MaximumTokens) + 1
			},
			want: domain.FailureManifestIntegrity,
		},
		{
			name: "processing version mismatch",
			configure: func(_ *stubBatchRepository, reader *stubShardReader, _ *stubDocumentEmbedder, _ *stubVectorIndex) {
				reader.chunks[0].ChunkingVersion = "token-window-v1"
			},
			want: domain.FailureManifestIntegrity,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &stubBatchRepository{metadata: BookProjection{BookID: "book-1", Title: "Systems", Author: "Author", Year: 2026}}
			reader := &stubShardReader{chunks: []Chunk{validChunk("Evidence")}}
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

func TestIndexerRejectsShardOrderAndTokenSequenceViolations(t *testing.T) {
	tests := []struct {
		name   string
		chunks []Chunk
	}{
		{
			name: "noncontiguous order",
			chunks: []Chunk{
				validChunk("First"),
				validChunkWithPosition("Second", 2, 2, 2, 1, 2),
			},
		},
		{
			name: "token gap",
			chunks: []Chunk{
				validChunk("First"),
				validChunkWithPosition("Second", 1, 2, 2, 5, 6),
			},
		},
		{
			name: "excessive overlap",
			chunks: []Chunk{
				validChunkWithPosition("First", 0, 1, 1, 200, 400),
				validChunkWithPosition("Second", 1, 2, 2, 100, 401),
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &stubBatchRepository{metadata: BookProjection{BookID: "book-1", Title: "Systems", Author: "Author", Year: 2026}}
			reader := &stubShardReader{chunks: test.chunks}
			embedder := &stubDocumentEmbedder{vectors: [][]float32{make([]float32, domain.EmbeddingDimensions), make([]float32, domain.EmbeddingDimensions)}}
			index := &stubVectorIndex{}
			indexer, err := NewIndexer(repository, reader, embedder, index, func() time.Time { return time.Date(2026, 7, 20, 10, 0, 0, 0, time.UTC) })
			if err != nil {
				t.Fatalf("NewIndexer() error = %v", err)
			}
			work := validBatchWork()
			work.ChunkCount = 2
			work.LastChunkOrder = 1

			err = indexer.Process(context.Background(), work)

			if got := FailureCategory(err); got != domain.FailureManifestIntegrity {
				t.Fatalf("FailureCategory() = %s, want %s, err=%v", got, domain.FailureManifestIntegrity, err)
			}
		})
	}
}

func TestIndexerKeepsArtifactOutagesRetryable(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want domain.FailureCategory
	}{
		{name: "artifact unavailable", err: ErrArtifactUnavailable, want: domain.FailureInternalIndexing},
		{name: "artifact timeout", err: errors.Join(ErrArtifactUnavailable, context.DeadlineExceeded), want: domain.FailureIndexingTimeout},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &stubBatchRepository{metadata: BookProjection{BookID: "book-1", Title: "Systems", Author: "Author", Year: 2026}}
			reader := &stubShardReader{err: test.err}
			embedder := &stubDocumentEmbedder{vectors: [][]float32{make([]float32, domain.EmbeddingDimensions)}}
			index := &stubVectorIndex{}
			indexer, err := NewIndexer(repository, reader, embedder, index, func() time.Time { return time.Date(2026, 7, 20, 10, 0, 0, 0, time.UTC) })
			if err != nil {
				t.Fatalf("NewIndexer() error = %v", err)
			}

			err = indexer.Process(context.Background(), validBatchWork())

			if got := FailureCategory(err); got != test.want {
				t.Fatalf("FailureCategory() = %s, want %s, err=%v", got, test.want, err)
			}
			if TerminalIndexingFailure(err) {
				t.Fatalf("TerminalIndexingFailure() = true for retryable error %v", err)
			}
		})
	}
}

func TestIndexerPreservesCompleteBatchTypedFailures(t *testing.T) {
	repository := &stubBatchRepository{metadata: BookProjection{BookID: "book-1", Title: "Systems", Author: "Author", Year: 2026}}
	repository.completeErr = Failure(domain.FailureManifestIntegrity, ErrConflictingEvent)
	reader := &stubShardReader{chunks: []Chunk{validChunk("Evidence")}}
	embedder := &stubDocumentEmbedder{vectors: [][]float32{make([]float32, domain.EmbeddingDimensions)}}
	index := &stubVectorIndex{}
	indexer, err := NewIndexer(repository, reader, embedder, index, func() time.Time { return time.Date(2026, 7, 20, 10, 0, 0, 0, time.UTC) })
	if err != nil {
		t.Fatalf("NewIndexer() error = %v", err)
	}

	err = indexer.Process(context.Background(), validBatchWork())

	if got := FailureCategory(err); got != domain.FailureManifestIntegrity {
		t.Fatalf("FailureCategory() = %s, want %s, err=%v", got, domain.FailureManifestIntegrity, err)
	}
	if !TerminalIndexingFailure(err) {
		t.Fatalf("TerminalIndexingFailure() = false for %v", err)
	}
}

func validBatchWork() BatchWork {
	profile := domain.SupportedIndexProfile()
	return BatchWork{EventID: "batch-event-1", JobID: "job-1", BatchID: "job-1:0", BookID: "book-1", ShardReference: "books/book-1/profile/shards/000000.pb.zst",
		ShardSHA256: sum(5), CompressedBytes: 10, UncompressedBytes: 20, ChunkCount: 1, ManifestPageCount: 2, SourceSHA256: sum(1), ManifestSHA256: sum(2),
		FirstChunkOrder: 0, LastChunkOrder: 0, ExtractionVersion: profile.ExtractionVersion, NormalizationVersion: profile.NormalizationVersion, TokenizerVersion: profile.TokenizerVersion,
		ChunkingVersion: profile.ChunkingVersion, StructureVersion: profile.StructureVersion, MaximumTokens: uint32(profile.MaximumTokens), OverlapTokens: uint32(profile.OverlapTokens), ProfileDigest: profile.Digest,
		CorrelationID: "correlation-1", CausationID: "manifest-1", Producer: "retrieval-service", SchemaVersion: "v1", IdempotencyKey: "job-1:0",
		OccurredAt: time.Date(2026, 7, 20, 9, 0, 0, 0, time.UTC)}
}

func validChunk(text string) Chunk {
	return validChunkWithPosition(text, 0, 1, 1, 0, 1)
}

func validChunkWithPosition(text string, order uint64, pageStart, pageEnd uint32, tokenStart, tokenEnd uint64) Chunk {
	return Chunk{
		ChunkID:              fmt.Sprintf("chunk-%d", order+1),
		BookID:               "book-1",
		Order:                order,
		Text:                 text,
		ContentSHA256:        sha256.Sum256([]byte(text)),
		PageStart:            pageStart,
		PageEnd:              pageEnd,
		TokenStart:           tokenStart,
		TokenEnd:             tokenEnd,
		ExtractionVersion:    "poppler-layout-v1",
		NormalizationVersion: "nfc-v1",
		TokenizerVersion:     "cl100k_base-v1",
		ChunkingVersion:      "token-window-v2",
		StructureVersion:     "heading-carry-v1",
	}
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
