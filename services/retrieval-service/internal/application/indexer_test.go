package application

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"
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
	if len(index.calls) != 2 || index.calls[0][0].EvidenceID != index.calls[1][0].EvidenceID || repository.completed != 1 {
		t.Fatalf("replay was not stable: calls=%#v completed=%d", index.calls, repository.completed)
	}
}

func validBatchWork() BatchWork {
	profile := supportedDigest()
	return BatchWork{EventID: "batch-event-1", JobID: "job-1", BatchID: "job-1:0", BookID: "book-1", ShardReference: "books/book-1/profile/shards/000000.pb.zst",
		ShardSHA256: sum(5), CompressedBytes: 10, UncompressedBytes: 20, ChunkCount: 1, SourceSHA256: sum(1), ManifestSHA256: sum(2),
		ProfileDigest: profile, CorrelationID: "correlation-1", CausationID: "manifest-1", Producer: "retrieval-service", SchemaVersion: "v1",
		IdempotencyKey: "job-1:0", OccurredAt: time.Date(2026, 7, 20, 9, 0, 0, 0, time.UTC)}
}

func supportedDigest() [32]byte {
	return [32]byte{0x7c, 0x98, 0x6c, 0xd0, 0xd5, 0xee, 0xd1, 0x7f, 0x39, 0x83, 0x29, 0xc4, 0xa0, 0x9e, 0xdb, 0x7d, 0x79, 0x09, 0x30, 0x9f, 0x12, 0x74, 0xe1, 0xb4, 0xef, 0x17, 0x66, 0x39, 0x73, 0x11, 0x68, 0x1c}
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
func (s *stubBatchRepository) FinalizeJob(context.Context, BatchWork, time.Time) error { return nil }

type stubShardReader struct{ chunks []Chunk }

func (s *stubShardReader) ReadShard(context.Context, BatchWork) ([]Chunk, error) {
	return s.chunks, nil
}

type stubDocumentEmbedder struct{ vectors [][]float32 }

func (s *stubDocumentEmbedder) EmbedDocuments(context.Context, []string) ([][]float32, error) {
	return s.vectors, nil
}

type stubVectorIndex struct{ calls [][]EvidenceRecord }

func (s *stubVectorIndex) Upsert(_ context.Context, values []EvidenceRecord) error {
	copyValues := append([]EvidenceRecord(nil), values...)
	s.calls = append(s.calls, copyValues)
	return nil
}
func (s *stubVectorIndex) ActivateJob(context.Context, string) error { return nil }
