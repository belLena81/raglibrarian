package application

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"math"
	"time"
	"unicode/utf8"

	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/domain"
)

type BatchWork struct {
	EventID, JobID, BatchID, BookID, ShardReference, CorrelationID, CausationID, Producer, SchemaVersion, IdempotencyKey string
	ExtractionVersion, NormalizationVersion, TokenizerVersion, ChunkingVersion, StructureVersion                         string
	ShardSHA256, SourceSHA256, ManifestSHA256, ProfileDigest                                                             [32]byte
	CompressedBytes, UncompressedBytes                                                                                   int64
	ChunkCount, ManifestPageCount, MaximumTokens, OverlapTokens                                                          uint32
	FirstChunkOrder, LastChunkOrder                                                                                      uint64
	LifecycleVersion                                                                                                     uint64
	OccurredAt                                                                                                           time.Time
}

func (w BatchWork) Validate() error {
	profile, ok := domain.SupportedIndexProfileForExtraction(w.ExtractionVersion)
	if !ok {
		return ErrInvalidEvent
	}
	if !safeID(w.EventID) || !safeID(w.JobID) || !safeID(w.BatchID) || !safeID(w.BookID) || !validArtifactReference(w.ShardReference) ||
		!safeID(w.CorrelationID) || !safeID(w.CausationID) || w.Producer != "retrieval-service" || w.SchemaVersion != "v1" ||
		w.IdempotencyKey != w.BatchID || w.ShardSHA256 == ([32]byte{}) || w.SourceSHA256 == ([32]byte{}) ||
		w.ManifestSHA256 == ([32]byte{}) || w.ProfileDigest != profile.Digest || w.CompressedBytes < 1 ||
		w.CompressedBytes > 32<<20 || w.UncompressedBytes < 1 || w.UncompressedBytes > 64<<20 ||
		w.ChunkCount < 1 || w.ChunkCount > 256 || w.ManifestPageCount < 1 || w.ManifestPageCount > maxManifestPages ||
		w.MaximumTokens == 0 || int64(w.MaximumTokens) != int64(profile.MaximumTokens) || int64(w.OverlapTokens) != int64(profile.OverlapTokens) ||
		w.ExtractionVersion != profile.ExtractionVersion || w.NormalizationVersion != profile.NormalizationVersion ||
		w.TokenizerVersion != profile.TokenizerVersion || w.ChunkingVersion != profile.ChunkingVersion || w.StructureVersion != profile.StructureVersion ||
		w.OccurredAt.IsZero() {
		return ErrInvalidEvent
	}
	expectedLastOrder, ok := shardLastOrder(w.FirstChunkOrder, w.ChunkCount)
	if !ok || w.LastChunkOrder != expectedLastOrder {
		return ErrInvalidEvent
	}
	return nil
}

type BookProjection struct {
	BookID, Title, Author, MediaType string
	Year                             int
	Tags                             []string
	ResumeFinalization               bool
}

type Chunk struct {
	ChunkID, BookID, Text, Chapter, Section                                                      string
	ExtractionVersion, NormalizationVersion, TokenizerVersion, ChunkingVersion, StructureVersion string
	ContentSHA256                                                                                [32]byte
	PageStart, PageEnd                                                                           uint32
	Order, TokenStart, TokenEnd                                                                  uint64
}

type EvidenceRecord struct {
	Evidence
	JobID            string
	ContentSHA256    [32]byte
	LifecycleVersion uint64
	Vector           []float32
}

type DocumentRecord struct {
	DocumentResult
	Vector           []float32
	LifecycleVersion uint64
}

type BatchRepository interface {
	BeginBatch(context.Context, BatchWork) (BookProjection, bool, error)
	CheckBatchActive(context.Context, BatchWork) (bool, error)
	CompleteBatch(context.Context, BatchWork, []EvidenceRecord, time.Time) (bool, error)
	DocumentRecord(context.Context, BatchWork) (DocumentRecord, error)
	PriorIndexedJobIDs(context.Context, BatchWork) ([]string, error)
	FinalizeJob(context.Context, BatchWork, time.Time) error
}

type ShardReader interface {
	ReadShard(context.Context, BatchWork) ([]Chunk, error)
}

type DocumentEmbedder interface {
	EmbedDocuments(context.Context, []string) ([][]float32, error)
}

type VectorIndex interface {
	UpsertChunks(context.Context, []EvidenceRecord) error
	UpsertDocument(context.Context, DocumentRecord) error
	ActivateJob(context.Context, string) error
	DeactivateJob(context.Context, string) error
	DeleteJob(context.Context, string) error
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
	if metadata.ResumeFinalization {
		return i.finalizeDocument(ctx, work)
	}
	chunks, err := i.reader.ReadShard(ctx, work)
	if err != nil {
		if errors.Is(err, ErrArtifactUnavailable) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return errors.Join(errors.New("read shard"), err)
		}
		return Failure(domain.FailureManifestIntegrity, errors.Join(errors.New("read shard"), err))
	}
	if len(chunks) != int(work.ChunkCount) {
		return Failure(domain.FailureManifestIntegrity, errors.New("invalid shard chunk count"))
	}
	texts := make([]string, len(chunks))
	nextOrder := work.FirstChunkOrder
	var previous Chunk
	for index, chunk := range chunks {
		if chunk.BookID != work.BookID || !safeID(chunk.ChunkID) || chunk.Text == "" || !utf8.ValidString(chunk.Text) ||
			!utf8.ValidString(chunk.Chapter) || !utf8.ValidString(chunk.Section) || chunk.ContentSHA256 != sha256.Sum256([]byte(chunk.Text)) ||
			chunk.PageStart < 1 || chunk.PageEnd < chunk.PageStart || chunk.PageEnd > work.ManifestPageCount ||
			chunk.Order != nextOrder || chunk.TokenEnd <= chunk.TokenStart ||
			chunk.TokenEnd-chunk.TokenStart > uint64(work.MaximumTokens) ||
			chunk.ExtractionVersion != work.ExtractionVersion || chunk.NormalizationVersion != work.NormalizationVersion ||
			chunk.TokenizerVersion != work.TokenizerVersion || chunk.ChunkingVersion != work.ChunkingVersion ||
			chunk.StructureVersion != work.StructureVersion {
			return Failure(domain.FailureManifestIntegrity, errors.New("invalid chunk"))
		}
		if index > 0 {
			if chunk.TokenStart < previous.TokenStart || chunk.TokenEnd <= previous.TokenEnd || chunk.TokenStart > previous.TokenEnd ||
				previous.TokenEnd-chunk.TokenStart > uint64(work.OverlapTokens) {
				return Failure(domain.FailureManifestIntegrity, errors.New("invalid chunk"))
			}
		}
		if len(chunk.Text) > 32<<10 || len(chunk.Chapter) > 1024 || len(chunk.Section) > 1024 {
			return Failure(domain.FailureResourceLimit, errors.New("invalid chunk"))
		}
		texts[index] = chunk.Text
		previous = chunk
		nextOrder++
	}
	if nextOrder != work.LastChunkOrder+1 {
		return Failure(domain.FailureManifestIntegrity, errors.New("invalid chunk"))
	}
	vectors, err := i.embedder.EmbedDocuments(ctx, texts)
	if err != nil || len(vectors) != len(chunks) {
		return Failure(domain.FailureEmbeddingUnavailable, errors.Join(errors.New("embed shard"), err))
	}
	records := make([]EvidenceRecord, len(chunks))
	mediaType := metadata.MediaType
	if mediaType == "" {
		mediaType = domain.MediaTypePDF
	}
	for index, chunk := range chunks {
		if len(vectors[index]) != domain.EmbeddingDimensions {
			return Failure(domain.FailureEmbeddingUnavailable, errors.New("invalid embedding dimensions"))
		}
		records[index] = EvidenceRecord{Evidence: Evidence{EvidenceID: work.JobID + ":" + chunk.ChunkID, ChunkID: chunk.ChunkID,
			BookID: work.BookID, Title: metadata.Title, Author: metadata.Author, MediaType: mediaType, Year: metadata.Year, Tags: append([]string(nil), metadata.Tags...),
			Chapter: chunk.Chapter, Section: chunk.Section, PageStart: chunk.PageStart, PageEnd: chunk.PageEnd, Passage: chunk.Text},
			JobID: work.JobID, ContentSHA256: chunk.ContentSHA256, LifecycleVersion: effectiveLifecycleVersion(work.LifecycleVersion), Vector: append([]float32(nil), vectors[index]...)}
	}
	active, err := i.repository.CheckBatchActive(ctx, work)
	if err != nil {
		return fmt.Errorf("check batch lifecycle: %w", err)
	}
	if !active {
		return ErrConflictingEvent
	}
	if err = i.index.UpsertChunks(ctx, records); err != nil {
		return Failure(domain.FailureVectorStoreUnavailable, errors.Join(errors.New("upsert vectors"), err))
	}
	completed, err := i.repository.CompleteBatch(ctx, work, records, i.now().UTC())
	if err != nil {
		return fmt.Errorf("complete batch: %w", err)
	}
	if completed {
		return i.finalizeDocument(ctx, work)
	}
	return nil
}

func (i *Indexer) finalizeDocument(ctx context.Context, work BatchWork) error {
	priorJobIDs, err := i.repository.PriorIndexedJobIDs(ctx, work)
	if err != nil {
		return errors.Join(errors.New("resolve prior index generations"), err)
	}
	document, err := i.repository.DocumentRecord(ctx, work)
	if err != nil {
		return errors.New("build document vector")
	}
	if len(document.Vector) != domain.EmbeddingDimensions || !normalized(document.Vector) {
		return Failure(domain.FailureEmbeddingUnavailable, errors.New("invalid document embedding"))
	}
	if err = i.index.UpsertDocument(ctx, document); err != nil {
		return Failure(domain.FailureVectorStoreUnavailable, errors.Join(errors.New("upsert document vector"), err))
	}
	if err = i.index.ActivateJob(ctx, work.JobID); err != nil {
		return Failure(domain.FailureVectorStoreUnavailable, errors.Join(errors.New("activate vectors"), err))
	}
	for _, priorJobID := range priorJobIDs {
		if err = i.index.DeleteJob(ctx, priorJobID); err != nil {
			return Failure(domain.FailureVectorStoreUnavailable, errors.Join(errors.New("delete prior index generation"), err))
		}
	}
	if err = i.repository.FinalizeJob(ctx, work, i.now().UTC()); err != nil {
		return errors.Join(errors.New("finalize index job"), err)
	}
	return nil
}

func NormalizedCentroid(vectors [][]float32) ([]float32, error) {
	if len(vectors) == 0 {
		return nil, errors.New("empty document embedding")
	}
	sum := make([]float32, domain.EmbeddingDimensions)
	for _, vector := range vectors {
		if len(vector) != domain.EmbeddingDimensions {
			return nil, errors.New("invalid embedding dimensions")
		}
		for index, value := range vector {
			sum[index] += value
		}
	}
	for index := range sum {
		sum[index] /= float32(len(vectors))
	}
	return normalizeVector(sum)
}

func normalizeVector(vector []float32) ([]float32, error) {
	var norm float64
	for _, value := range vector {
		norm += float64(value) * float64(value)
	}
	if norm == 0 {
		return nil, errors.New("empty document embedding")
	}
	scale := float32(1 / math.Sqrt(norm))
	result := make([]float32, len(vector))
	for index, value := range vector {
		result[index] = value * scale
	}
	return result, nil
}

func normalized(vector []float32) bool {
	var norm float64
	for _, value := range vector {
		norm += float64(value) * float64(value)
	}
	return math.Abs(norm-1) <= 0.001
}
