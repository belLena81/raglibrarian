package application

import (
	"context"
	"crypto/sha256"
	"errors"
	"time"
	"unicode/utf8"

	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/domain"
)

type BatchWork struct {
	EventID, JobID, BatchID, BookID, ShardReference, CorrelationID, CausationID, Producer, SchemaVersion, IdempotencyKey string
	ShardSHA256, SourceSHA256, ManifestSHA256, ProfileDigest                                                             [32]byte
	CompressedBytes, UncompressedBytes                                                                                   int64
	ChunkCount                                                                                                           uint32
	OccurredAt                                                                                                           time.Time
}

func (w BatchWork) Validate() error {
	profile := domain.SupportedIndexProfile()
	if !safeID(w.EventID) || !safeID(w.JobID) || !safeID(w.BatchID) || !safeID(w.BookID) || !validArtifactReference(w.ShardReference) ||
		!safeID(w.CorrelationID) || !safeID(w.CausationID) || w.Producer != "retrieval-service" || w.SchemaVersion != "v1" ||
		w.IdempotencyKey != w.BatchID || w.ShardSHA256 == ([32]byte{}) || w.SourceSHA256 == ([32]byte{}) ||
		w.ManifestSHA256 == ([32]byte{}) || w.ProfileDigest != profile.Digest || w.CompressedBytes < 1 ||
		w.CompressedBytes > 32<<20 || w.UncompressedBytes < 1 || w.UncompressedBytes > 64<<20 ||
		w.ChunkCount < 1 || w.ChunkCount > 256 || w.OccurredAt.IsZero() {
		return ErrInvalidEvent
	}
	return nil
}

type BookProjection struct {
	BookID, Title, Author string
	Year                  int
	Tags                  []string
}

type Chunk struct {
	ChunkID, BookID, Text, Chapter, Section string
	ContentSHA256                           [32]byte
	PageStart, PageEnd                      uint32
}

type EvidenceRecord struct {
	Evidence
	JobID         string
	ContentSHA256 [32]byte
	Vector        []float32
}

type BatchRepository interface {
	BeginBatch(context.Context, BatchWork) (BookProjection, bool, error)
	CompleteBatch(context.Context, BatchWork, []EvidenceRecord, time.Time) (bool, error)
	FinalizeJob(context.Context, BatchWork, time.Time) error
}

type ShardReader interface {
	ReadShard(context.Context, BatchWork) ([]Chunk, error)
}

type DocumentEmbedder interface {
	EmbedDocuments(context.Context, []string) ([][]float32, error)
}

type VectorIndex interface {
	Upsert(context.Context, []EvidenceRecord) error
	ActivateJob(context.Context, string) error
}

type Indexer struct {
	repository BatchRepository
	reader     ShardReader
	embedder   DocumentEmbedder
	index      VectorIndex
	now        func() time.Time
}

func NewIndexer(repository BatchRepository, reader ShardReader, embedder DocumentEmbedder, index VectorIndex, now func() time.Time) (*Indexer, error) {
	if repository == nil || reader == nil || embedder == nil || index == nil || now == nil {
		return nil, errors.New("invalid indexer configuration")
	}
	return &Indexer{repository: repository, reader: reader, embedder: embedder, index: index, now: now}, nil
}

func (i *Indexer) Process(ctx context.Context, work BatchWork) error {
	if err := work.Validate(); err != nil {
		return err
	}
	metadata, accepted, err := i.repository.BeginBatch(ctx, work)
	if err != nil || !accepted {
		return err
	}
	chunks, err := i.reader.ReadShard(ctx, work)
	if err != nil {
		return errors.New("read shard")
	}
	if len(chunks) != int(work.ChunkCount) {
		return errors.New("invalid shard chunk count")
	}
	texts := make([]string, len(chunks))
	for index, chunk := range chunks {
		if chunk.BookID != work.BookID || !safeID(chunk.ChunkID) || chunk.Text == "" || !utf8.ValidString(chunk.Text) || len(chunk.Text) > 32<<10 ||
			!utf8.ValidString(chunk.Chapter) || len(chunk.Chapter) > 1024 || !utf8.ValidString(chunk.Section) || len(chunk.Section) > 1024 ||
			chunk.ContentSHA256 != sha256.Sum256([]byte(chunk.Text)) || chunk.PageEnd < chunk.PageStart {
			return errors.New("invalid chunk")
		}
		texts[index] = chunk.Text
	}
	vectors, err := i.embedder.EmbedDocuments(ctx, texts)
	if err != nil || len(vectors) != len(chunks) {
		return errors.New("embed shard")
	}
	records := make([]EvidenceRecord, len(chunks))
	for index, chunk := range chunks {
		if len(vectors[index]) != domain.EmbeddingDimensions {
			return errors.New("invalid embedding dimensions")
		}
		records[index] = EvidenceRecord{Evidence: Evidence{EvidenceID: work.BookID + ":" + chunk.ChunkID, ChunkID: chunk.ChunkID,
			BookID: work.BookID, Title: metadata.Title, Author: metadata.Author, Year: metadata.Year, Tags: append([]string(nil), metadata.Tags...),
			Chapter: chunk.Chapter, Section: chunk.Section, PageStart: chunk.PageStart, PageEnd: chunk.PageEnd, Passage: chunk.Text},
			JobID: work.JobID, ContentSHA256: chunk.ContentSHA256, Vector: append([]float32(nil), vectors[index]...)}
	}
	if err = i.index.Upsert(ctx, records); err != nil {
		return errors.New("upsert vectors")
	}
	completed, err := i.repository.CompleteBatch(ctx, work, records, i.now().UTC())
	if err != nil {
		return errors.New("complete batch")
	}
	if completed {
		if err = i.index.ActivateJob(ctx, work.JobID); err != nil {
			return errors.New("activate vectors")
		}
		if err = i.repository.FinalizeJob(ctx, work, i.now().UTC()); err != nil {
			return errors.New("finalize index job")
		}
	}
	return nil
}
