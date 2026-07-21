// Package repository implements Retrieval-owned PostgreSQL persistence.
package repository

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	ingestionv1 "github.com/belLena81/raglibrarian/pkg/proto/ingestion/v1"
	retrievalv1 "github.com/belLena81/raglibrarian/pkg/proto/retrieval/v1"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/application"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type Postgres struct {
	pool *pgxpool.Pool
}

type OutboxRecord struct {
	EventID, EventType string
	Payload            []byte
}

type VectorCleanupJob struct {
	JobID  string
	BookID string
}

func NewPostgres(pool *pgxpool.Pool) *Postgres {
	if pool == nil {
		panic("retrieval repository: pool is required")
	}
	return &Postgres{pool: pool}
}

func (r *Postgres) CheckReady(ctx context.Context) error {
	return r.pool.Ping(ctx)
}

func (r *Postgres) ProjectMetadata(ctx context.Context, event application.MetadataEvent) (application.PlanningSnapshot, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return application.PlanningSnapshot{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = lockBookProjection(ctx, tx, event.BookID); err != nil {
		return application.PlanningSnapshot{}, err
	}
	command, err := tx.Exec(ctx, `INSERT INTO retrieval.metadata_facts
		(book_id,event_id,payload_digest,source_sha256,title,author,publication_year,tags,correlation_id,causation_id,occurred_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) ON CONFLICT DO NOTHING`, event.BookID, event.EventID, event.PayloadDigest[:], event.SourceSHA256[:], event.Title, event.Author, event.Year, event.Tags, event.CorrelationID, event.CausationID, event.OccurredAt)
	if err != nil {
		return application.PlanningSnapshot{}, fmt.Errorf("retrieval: project metadata: %w", err)
	}
	if command.RowsAffected() == 0 {
		var digest []byte
		if err = tx.QueryRow(ctx, `SELECT payload_digest FROM retrieval.metadata_facts WHERE book_id=$1 OR event_id=$2`, event.BookID, event.EventID).Scan(&digest); err != nil || !equalDigest(digest, event.PayloadDigest) {
			return application.PlanningSnapshot{}, application.ErrConflictingEvent
		}
	}
	if err = r.materializeDeferredFailedManifest(ctx, tx, event.BookID); err != nil {
		return application.PlanningSnapshot{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return application.PlanningSnapshot{}, err
	}
	return r.loadSnapshot(ctx, event.BookID)
}

func (r *Postgres) ProjectManifest(ctx context.Context, event application.ManifestEvent) (application.PlanningSnapshot, error) {
	manifestPayload, err := encodeManifest(event.Manifest)
	if err != nil {
		return application.PlanningSnapshot{}, application.ErrInvalidEvent
	}
	command, err := r.pool.Exec(ctx, `INSERT INTO retrieval.manifest_facts
		(book_id,event_id,payload_digest,source_sha256,manifest_sha256,manifest_reference,manifest_payload,correlation_id,causation_id,occurred_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT DO NOTHING`, event.BookID, event.EventID, event.PayloadDigest[:], event.SourceSHA256[:], event.ManifestSHA256[:], event.ManifestReference, manifestPayload, event.CorrelationID, event.CausationID, event.OccurredAt)
	if err != nil {
		return application.PlanningSnapshot{}, fmt.Errorf("retrieval: project manifest: %w", err)
	}
	if command.RowsAffected() == 0 {
		var digest []byte
		if err = r.pool.QueryRow(ctx, `SELECT payload_digest FROM retrieval.manifest_facts WHERE book_id=$1 OR event_id=$2`, event.BookID, event.EventID).Scan(&digest); err != nil || !equalDigest(digest, event.PayloadDigest) {
			return application.PlanningSnapshot{}, application.ErrConflictingEvent
		}
	}
	return r.loadSnapshot(ctx, event.BookID)
}

func (r *Postgres) loadSnapshot(ctx context.Context, bookID string) (application.PlanningSnapshot, error) {
	snapshot := application.PlanningSnapshot{}
	metadata := application.MetadataEvent{BookID: bookID, Producer: "catalog-service", SchemaVersion: "v1", IdempotencyKey: bookID}
	var metadataDigest, metadataSource []byte
	err := r.pool.QueryRow(ctx, `SELECT event_id,payload_digest,source_sha256,title,author,publication_year,tags,correlation_id,causation_id,occurred_at FROM retrieval.metadata_facts WHERE book_id=$1`, bookID).
		Scan(&metadata.EventID, &metadataDigest, &metadataSource, &metadata.Title, &metadata.Author, &metadata.Year, &metadata.Tags, &metadata.CorrelationID, &metadata.CausationID, &metadata.OccurredAt)
	if err == nil {
		copy(metadata.PayloadDigest[:], metadataDigest)
		copy(metadata.SourceSHA256[:], metadataSource)
		snapshot.Metadata = &metadata
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return application.PlanningSnapshot{}, err
	}
	manifest := application.ManifestEvent{BookID: bookID, Producer: "ingestion-service", SchemaVersion: "v1"}
	var manifestDigest, manifestSource, manifestSHA256, manifestPayload []byte
	var manifestFailureCategory string
	err = r.pool.QueryRow(ctx, `SELECT event_id,payload_digest,source_sha256,manifest_sha256,manifest_reference,manifest_payload,correlation_id,causation_id,occurred_at,coalesce(failure_category,'')
		FROM retrieval.manifest_facts WHERE book_id=$1`, bookID).
		Scan(&manifest.EventID, &manifestDigest, &manifestSource, &manifestSHA256, &manifest.ManifestReference, &manifestPayload, &manifest.CorrelationID, &manifest.CausationID, &manifest.OccurredAt, &manifestFailureCategory)
	if err == nil {
		manifest.IdempotencyKey = bookID + ":stored:ready"
		copy(manifest.PayloadDigest[:], manifestDigest)
		copy(manifest.SourceSHA256[:], manifestSource)
		copy(manifest.ManifestSHA256[:], manifestSHA256)
		if manifestFailureCategory == "" {
			manifest.Manifest, err = decodeManifest(manifestPayload, manifest.ManifestSHA256)
			if err != nil {
				return application.PlanningSnapshot{}, err
			}
			snapshot.Manifest = &manifest
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return application.PlanningSnapshot{}, err
	}
	if snapshot.Metadata != nil && snapshot.Manifest != nil {
		err = r.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM retrieval.index_jobs WHERE book_id=$1 AND source_sha256=$2 AND manifest_sha256=$3)`, bookID, snapshot.Manifest.SourceSHA256[:], snapshot.Manifest.ManifestSHA256[:]).Scan(&snapshot.Planned)
	}
	return snapshot, err
}

func (r *Postgres) CommitPlan(ctx context.Context, snapshot application.PlanningSnapshot, batches []application.BatchPlan) (bool, error) {
	if snapshot.Metadata == nil || snapshot.Manifest == nil || len(batches) == 0 {
		return false, application.ErrInvalidEvent
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	first := batches[0]
	command, err := tx.Exec(ctx, `INSERT INTO retrieval.index_jobs
		(id,book_id,source_sha256,manifest_sha256,profile_digest,state,expected_batches,correlation_id,created_at,updated_at)
		VALUES($1,$2,$3,$4,$5,'pending',$6,$7,$8,$8) ON CONFLICT DO NOTHING`, first.JobID, first.BookID, snapshot.Manifest.SourceSHA256[:], snapshot.Manifest.ManifestSHA256[:], first.ProfileDigest[:], len(batches), snapshot.Manifest.CorrelationID, first.OccurredAt)
	if err != nil {
		return false, err
	}
	if command.RowsAffected() == 0 {
		return false, tx.Commit(ctx)
	}
	for _, batch := range batches {
		if batch.JobID != first.JobID || batch.BookID != first.BookID || batch.ProfileDigest != first.ProfileDigest {
			return false, application.ErrConflictingEvent
		}
		_, err = tx.Exec(ctx, `INSERT INTO retrieval.index_batches
			(id,job_id,shard_reference,shard_sha256,compressed_byte_size,uncompressed_byte_size,chunk_count,updated_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, batch.BatchID, batch.JobID, batch.Reference, batch.SHA256[:], batch.CompressedBytes, batch.UncompressedBytes, batch.ChunkCount, batch.OccurredAt)
		if err != nil {
			return false, err
		}
		message := &retrievalv1.IndexBatchRequestedV1{EventId: batch.BatchID + ":requested", JobId: batch.JobID, BatchId: batch.BatchID, BookId: batch.BookID,
			ShardReference: batch.Reference, ShardSha256: batch.SHA256[:], CompressedByteSize: batch.CompressedBytes, UncompressedByteSize: batch.UncompressedBytes,
			ChunkCount: batch.ChunkCount, SourceSha256: snapshot.Manifest.SourceSHA256[:], ManifestSha256: snapshot.Manifest.ManifestSHA256[:], IndexProfileDigest: batch.ProfileDigest[:],
			FirstChunkOrder: batch.FirstChunkOrder, LastChunkOrder: batch.LastChunkOrder, ManifestPageCount: batch.ManifestPageCount, ExtractionVersion: batch.ExtractionVersion,
			NormalizationVersion: batch.NormalizationVersion, TokenizerVersion: batch.TokenizerVersion, ChunkingVersion: batch.ChunkingVersion, StructureVersion: batch.StructureVersion,
			MaximumTokens: batch.MaximumTokens, OverlapTokens: batch.OverlapTokens, CorrelationId: snapshot.Manifest.CorrelationID, OccurredAt: timestamppb.New(batch.OccurredAt),
			CausationId: snapshot.Manifest.EventID, Producer: "retrieval-service", SchemaVersion: "v1", IdempotencyKey: batch.BatchID}
		payload, marshalErr := proto.MarshalOptions{Deterministic: true}.Marshal(message)
		if marshalErr != nil {
			return false, marshalErr
		}
		_, err = tx.Exec(ctx, `INSERT INTO retrieval.outbox(event_id,event_type,aggregate_id,payload,occurred_at,next_attempt_at) VALUES($1,'retrieval.index-batch.requested.v1',$2,$3,$4,$4)`, message.EventId, batch.JobID, payload, batch.OccurredAt)
		if err != nil {
			return false, err
		}
	}
	return true, tx.Commit(ctx)
}

func encodeManifest(value application.Manifest) ([]byte, error) {
	message := &ingestionv1.ChunkManifestV1{SchemaVersion: value.SchemaVersion, BookId: value.BookID, SourceSha256: value.SourceSHA256[:], ProcessingConfigDigest: value.ProcessingConfigDigest[:], ExtractionVersion: value.ExtractionVersion,
		NormalizationVersion: value.NormalizationVersion, TokenizerVersion: value.TokenizerVersion, ChunkingVersion: value.ChunkingVersion, StructureVersion: value.StructureVersion,
		MaximumTokens: value.MaximumTokens, OverlapTokens: value.OverlapTokens, PageCount: value.PageCount, ChunkCount: value.ChunkCount, GeneratedAt: timestamppb.New(value.GeneratedAt), Shards: make([]*ingestionv1.ChunkShardDescriptorV1, len(value.Shards))}
	for index, shard := range value.Shards {
		message.Shards[index] = &ingestionv1.ChunkShardDescriptorV1{Reference: shard.Reference, Sha256: shard.SHA256[:], CompressedByteSize: shard.CompressedBytes, UncompressedByteSize: shard.UncompressedBytes,
			ChunkCount: shard.ChunkCount, FirstChunkOrder: shard.FirstChunkOrder, LastChunkOrder: shard.LastChunkOrder}
	}
	return proto.MarshalOptions{Deterministic: true}.Marshal(message)
}

func decodeManifest(payload []byte, digest [32]byte) (application.Manifest, error) {
	var message ingestionv1.ChunkManifestV1
	if err := (proto.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(payload, &message); err != nil {
		return application.Manifest{}, err
	}
	if len(message.ProcessingConfigDigest) != 32 || message.GeneratedAt == nil || !message.GeneratedAt.IsValid() {
		return application.Manifest{}, application.ErrInvalidEvent
	}
	value := application.Manifest{SchemaVersion: message.SchemaVersion, BookID: message.BookId, ExtractionVersion: message.ExtractionVersion, NormalizationVersion: message.NormalizationVersion,
		TokenizerVersion: message.TokenizerVersion, ChunkingVersion: message.ChunkingVersion, StructureVersion: message.StructureVersion, MaximumTokens: message.MaximumTokens, OverlapTokens: message.OverlapTokens,
		ManifestSHA256: digest, ProcessingConfigDigest: digestBytes(message.ProcessingConfigDigest), PageCount: message.PageCount, ChunkCount: message.ChunkCount, GeneratedAt: message.GeneratedAt.AsTime(), Shards: make([]application.Shard, len(message.Shards))}
	copy(value.SourceSHA256[:], message.SourceSha256)
	for index, shard := range message.Shards {
		if shard == nil || len(shard.Sha256) != 32 {
			return application.Manifest{}, application.ErrInvalidEvent
		}
		value.Shards[index] = application.Shard{Reference: shard.Reference, CompressedBytes: shard.CompressedByteSize, UncompressedBytes: shard.UncompressedByteSize,
			ChunkCount: shard.ChunkCount, FirstChunkOrder: shard.FirstChunkOrder, LastChunkOrder: shard.LastChunkOrder}
		copy(value.Shards[index].SHA256[:], shard.Sha256)
	}
	return value, nil
}

func equalDigest(value []byte, expected [32]byte) bool {
	return len(value) == len(expected) && string(value) == string(expected[:])
}

func digestBytes(value []byte) [32]byte {
	var digest [32]byte
	copy(digest[:], value)
	return digest
}

func (r *Postgres) BeginBatch(ctx context.Context, work application.BatchWork) (application.BookProjection, bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return application.BookProjection{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var state, jobState, bookID, title, author string
	var year int
	var tags []string
	var shardSHA256, sourceSHA256, manifestSHA256, profileDigest, manifestPayload []byte
	var compressedBytes, uncompressedBytes int64
	var chunkCount int
	err = tx.QueryRow(ctx, `SELECT b.state,j.state,j.book_id,m.title,m.author,m.publication_year,m.tags,b.shard_sha256,b.compressed_byte_size,b.uncompressed_byte_size,b.chunk_count,j.source_sha256,j.manifest_sha256,j.profile_digest,f.manifest_payload
		FROM retrieval.index_batches b
		JOIN retrieval.index_jobs j ON j.id=b.job_id
		JOIN retrieval.metadata_facts m ON m.book_id=j.book_id
		JOIN retrieval.manifest_facts f ON f.book_id=j.book_id AND f.manifest_sha256=j.manifest_sha256
		WHERE b.id=$1 AND b.job_id=$2 FOR UPDATE OF b`, work.BatchID, work.JobID).
		Scan(&state, &jobState, &bookID, &title, &author, &year, &tags, &shardSHA256, &compressedBytes, &uncompressedBytes, &chunkCount, &sourceSHA256, &manifestSHA256, &profileDigest, &manifestPayload)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return application.BookProjection{}, false, application.ErrInvalidEvent
		}
		return application.BookProjection{}, false, err
	}
	if bookID != work.BookID || !equalDigest(shardSHA256, work.ShardSHA256) || !equalDigest(sourceSHA256, work.SourceSHA256) ||
		!equalDigest(manifestSHA256, work.ManifestSHA256) || !equalDigest(profileDigest, work.ProfileDigest) || compressedBytes != work.CompressedBytes ||
		uncompressedBytes != work.UncompressedBytes || chunkCount != int(work.ChunkCount) {
		return application.BookProjection{}, false, application.ErrConflictingEvent
	}
	manifest, err := decodeManifest(manifestPayload, digestBytes(manifestSHA256))
	if err != nil {
		return application.BookProjection{}, false, application.ErrConflictingEvent
	}
	bounds, ok := shardBounds(manifest, work)
	if !ok || work.ManifestPageCount != manifest.PageCount || work.FirstChunkOrder != bounds.FirstChunkOrder || work.LastChunkOrder != bounds.LastChunkOrder {
		return application.BookProjection{}, false, application.ErrConflictingEvent
	}
	if state == "failed" || jobState == "indexed" || jobState == "failed" {
		return application.BookProjection{}, false, tx.Commit(ctx)
	}
	if state == "completed" {
		return application.BookProjection{BookID: bookID, Title: title, Author: author, Year: year, Tags: tags}, true, tx.Commit(ctx)
	}
	_, err = tx.Exec(ctx, `UPDATE retrieval.index_batches SET state='processing',attempts=attempts+1,updated_at=now() WHERE id=$1`, work.BatchID)
	if err != nil {
		return application.BookProjection{}, false, err
	}
	return application.BookProjection{BookID: bookID, Title: title, Author: author, Year: year, Tags: tags}, true, tx.Commit(ctx)
}

func shardBounds(manifest application.Manifest, work application.BatchWork) (application.Shard, bool) {
	for _, shard := range manifest.Shards {
		if shard.Reference != work.ShardReference || shard.SHA256 != work.ShardSHA256 || shard.CompressedBytes != work.CompressedBytes ||
			shard.UncompressedBytes != work.UncompressedBytes || shard.ChunkCount != work.ChunkCount {
			continue
		}
		return shard, true
	}
	return application.Shard{}, false
}

func (r *Postgres) CompleteBatch(ctx context.Context, work application.BatchWork, records []application.EvidenceRecord, now time.Time) (bool, error) {
	if len(records) == 0 {
		return false, application.ErrInvalidEvent
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var jobState string
	if err = tx.QueryRow(ctx, `SELECT state FROM retrieval.index_jobs WHERE id=$1 FOR UPDATE`, work.JobID).Scan(&jobState); err != nil {
		return false, err
	}
	var state string
	if err = tx.QueryRow(ctx, `SELECT state FROM retrieval.index_batches WHERE id=$1 AND job_id=$2 FOR UPDATE`, work.BatchID, work.JobID).Scan(&state); err != nil {
		return false, err
	}
	if state == "completed" {
		var remaining int
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM retrieval.index_batches WHERE job_id=$1 AND state <> 'completed'`, work.JobID).Scan(&remaining); err != nil {
			return false, err
		}
		return remaining == 0, tx.Commit(ctx)
	}
	if state != "processing" {
		return false, application.ErrConflictingEvent
	}
	batchVectorSum := make([]float32, domain.EmbeddingDimensions)
	var pageStart, pageEnd uint32
	havePageStart := false
	seenChunkIDs := make(map[string]struct{}, len(records))
	for _, record := range records {
		if record.JobID != work.JobID || record.BookID != work.BookID || len(record.Vector) != domain.EmbeddingDimensions {
			return false, application.ErrInvalidEvent
		}
		if _, seen := seenChunkIDs[record.ChunkID]; seen {
			return false, application.Failure(domain.FailureManifestIntegrity, application.ErrConflictingEvent)
		}
		seenChunkIDs[record.ChunkID] = struct{}{}
		if !havePageStart || record.PageStart < pageStart {
			pageStart = record.PageStart
			havePageStart = true
		}
		if record.PageEnd > pageEnd {
			pageEnd = record.PageEnd
		}
		for index, value := range record.Vector {
			batchVectorSum[index] += value
		}
		command, err := tx.Exec(ctx, `INSERT INTO retrieval.evidence
			(evidence_id,chunk_id,job_id,book_id,title,author,publication_year,tags,chapter,section,page_start,page_end,passage,content_sha256,created_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
			ON CONFLICT(job_id,chunk_id) DO NOTHING`, record.EvidenceID, record.ChunkID, work.JobID, record.BookID, record.Title, record.Author,
			record.Year, record.Tags, record.Chapter, record.Section, record.PageStart, record.PageEnd, record.Passage, record.ContentSHA256[:], now)
		if err != nil {
			return false, err
		}
		if command.RowsAffected() != 1 {
			return false, application.Failure(domain.FailureManifestIntegrity, application.ErrConflictingEvent)
		}
	}
	if err = r.accumulateDocumentVector(ctx, tx, work.JobID, batchVectorSum, len(records), now); err != nil {
		return false, err
	}
	firstRecord := records[0]
	_, err = tx.Exec(ctx, `INSERT INTO retrieval.documents
		(document_id,job_id,book_id,title,author,publication_year,tags,chunk_count,page_start,page_end,created_at,updated_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$11)
		ON CONFLICT(job_id) DO UPDATE SET
			chunk_count=retrieval.documents.chunk_count + EXCLUDED.chunk_count,
			page_start=LEAST(retrieval.documents.page_start, EXCLUDED.page_start),
			page_end=GREATEST(retrieval.documents.page_end, EXCLUDED.page_end),
			updated_at=EXCLUDED.updated_at`, work.BookID+":"+work.JobID, work.JobID, work.BookID, firstRecord.Title, firstRecord.Author, firstRecord.Year, firstRecord.Tags, len(records), pageStart, pageEnd, now)
	if err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `UPDATE retrieval.index_batches SET state='completed',lease_owner=NULL,lease_expires_at=NULL,updated_at=$2 WHERE id=$1`, work.BatchID, now); err != nil {
		return false, err
	}
	var remaining int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM retrieval.index_batches WHERE job_id=$1 AND state <> 'completed'`, work.JobID).Scan(&remaining); err != nil {
		return false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return false, err
	}
	return remaining == 0, nil
}

type queryExecer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

type storedManifestFailure struct {
	event      application.ManifestEvent
	category   domain.FailureCategory
	recordedAt time.Time
}

func (r *Postgres) accumulateDocumentVector(ctx context.Context, tx queryExecer, jobID string, batchVectorSum []float32, chunkCount int, now time.Time) error {
	var vectorSum []float32
	var accumulated int
	err := tx.QueryRow(ctx, `SELECT vector_sum,chunk_count FROM retrieval.document_embedding_accumulators WHERE job_id=$1 FOR UPDATE`, jobID).Scan(&vectorSum, &accumulated)
	if errors.Is(err, pgx.ErrNoRows) {
		_, err = tx.Exec(ctx, `INSERT INTO retrieval.document_embedding_accumulators(job_id,vector_sum,chunk_count,updated_at) VALUES($1,$2,$3,$4)`, jobID, batchVectorSum, chunkCount, now)
		return err
	}
	if err != nil {
		return err
	}
	if len(vectorSum) != domain.EmbeddingDimensions {
		return application.ErrConflictingEvent
	}
	for index, value := range batchVectorSum {
		vectorSum[index] += value
	}
	_, err = tx.Exec(ctx, `UPDATE retrieval.document_embedding_accumulators SET vector_sum=$2,chunk_count=$3,updated_at=$4 WHERE job_id=$1`, jobID, vectorSum, accumulated+chunkCount, now)
	return err
}

func (r *Postgres) DocumentRecord(ctx context.Context, work application.BatchWork) (application.DocumentRecord, error) {
	var document application.DocumentRecord
	var vectorSum []float32
	var chunkCount int
	err := r.pool.QueryRow(ctx, `SELECT d.document_id,d.job_id,d.book_id,d.title,d.author,d.publication_year,d.tags,d.chunk_count,d.page_start,d.page_end,a.vector_sum,a.chunk_count
		FROM retrieval.documents d JOIN retrieval.document_embedding_accumulators a ON a.job_id=d.job_id
		WHERE d.job_id=$1`, work.JobID).Scan(&document.DocumentID, &document.JobID, &document.BookID, &document.Title, &document.Author, &document.Year, &document.Tags, &document.ChunkCount, &document.PageStart, &document.PageEnd, &vectorSum, &chunkCount)
	if err != nil {
		return application.DocumentRecord{}, err
	}
	if document.JobID != work.JobID || document.BookID != work.BookID || int(document.ChunkCount) != chunkCount || len(vectorSum) != domain.EmbeddingDimensions {
		return application.DocumentRecord{}, application.ErrConflictingEvent
	}
	vector, err := application.NormalizedCentroid([][]float32{vectorSum})
	if err != nil {
		return application.DocumentRecord{}, err
	}
	document.Vector = vector
	return document, nil
}

func (r *Postgres) FinalizeJob(ctx context.Context, work application.BatchWork, now time.Time) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var jobState string
	if err = tx.QueryRow(ctx, `SELECT state FROM retrieval.index_jobs WHERE id=$1 FOR UPDATE`, work.JobID).Scan(&jobState); err != nil {
		return err
	}
	var remaining, evidenceCount int
	if err = tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE state <> 'completed'),(SELECT count(*) FROM retrieval.evidence WHERE job_id=$1) FROM retrieval.index_batches WHERE job_id=$1`, work.JobID).Scan(&remaining, &evidenceCount); err != nil {
		return err
	}
	if remaining != 0 || evidenceCount < 1 || uint64(evidenceCount) > uint64(^uint32(0)) {
		return errors.New("index job is not complete")
	}
	command, err := tx.Exec(ctx, `UPDATE retrieval.index_jobs SET state='indexed',evidence_count=$2,updated_at=$3 WHERE id=$1 AND state='pending'`, work.JobID, evidenceCount, now)
	if err != nil {
		return err
	}
	if command.RowsAffected() == 1 {
		message := &retrievalv1.BookIndexedV1{EventId: work.JobID + ":indexed", BookId: work.BookID, JobId: work.JobID,
			SourceSha256: work.SourceSHA256[:], ManifestSha256: work.ManifestSHA256[:], IndexProfileDigest: work.ProfileDigest[:], EvidenceCount: uint32(evidenceCount), // #nosec G115 -- checked above.
			CorrelationId: work.CorrelationID, OccurredAt: timestamppb.New(now), CausationId: work.EventID, Producer: "retrieval-service", SchemaVersion: "v1", IdempotencyKey: work.JobID + ":indexed"}
		payload, marshalErr := proto.MarshalOptions{Deterministic: true}.Marshal(message)
		if marshalErr != nil {
			return marshalErr
		}
		_, err = tx.Exec(ctx, `INSERT INTO retrieval.outbox(event_id,event_type,aggregate_id,payload,occurred_at,next_attempt_at) VALUES($1,'retrieval.book.indexed.v1',$2,$3,$4,$4) ON CONFLICT DO NOTHING`, message.EventId, work.JobID, payload, now)
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (r *Postgres) FailBatch(ctx context.Context, work application.BatchWork, category domain.FailureCategory, now time.Time) (bool, error) {
	protoCategory, ok := failureProto(category)
	if !ok {
		return false, application.ErrInvalidEvent
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `UPDATE retrieval.index_batches SET state='failed',updated_at=$2 WHERE id=$1 AND state <> 'completed'`, work.BatchID, now); err != nil {
		return false, err
	}
	command, err := tx.Exec(ctx, `UPDATE retrieval.index_jobs
		SET state='failed',failure_category=$2,vector_cleanup_pending=true,vector_cleanup_attempts=0,vector_cleanup_next_attempt_at=$3,updated_at=$3
		WHERE id=$1 AND state='pending'`, work.JobID, string(category), now)
	if err != nil {
		return false, err
	}
	if command.RowsAffected() == 1 {
		message := &retrievalv1.BookIndexingFailedV1{EventId: work.JobID + ":failed", BookId: work.BookID, JobId: work.JobID,
			SourceSha256: work.SourceSHA256[:], ManifestSha256: work.ManifestSHA256[:], IndexProfileDigest: work.ProfileDigest[:], FailureCategory: protoCategory,
			CorrelationId: work.CorrelationID, OccurredAt: timestamppb.New(now), CausationId: work.EventID, Producer: "retrieval-service", SchemaVersion: "v1", IdempotencyKey: work.JobID + ":failed"}
		payload, marshalErr := proto.MarshalOptions{Deterministic: true}.Marshal(message)
		if marshalErr != nil {
			return false, marshalErr
		}
		_, err = tx.Exec(ctx, `INSERT INTO retrieval.outbox(event_id,event_type,aggregate_id,payload,occurred_at,next_attempt_at) VALUES($1,'retrieval.book.indexing-failed.v1',$2,$3,$4,$4) ON CONFLICT DO NOTHING`, message.EventId, work.JobID, payload, now)
		if err != nil {
			return false, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return false, err
	}
	return command.RowsAffected() == 1, nil
}

func (r *Postgres) FailManifest(ctx context.Context, event application.ManifestEvent, category domain.FailureCategory, now time.Time) error {
	protoCategory, ok := failureProto(category)
	if !ok {
		return application.ErrInvalidEvent
	}
	manifestPayload := []byte{}
	if category == domain.FailureManifestIntegrity {
		if err := event.ValidateEnvelope(); err != nil {
			return err
		}
	} else {
		if err := event.Validate(domain.SupportedIndexProfile()); !errors.Is(err, application.ErrUnsupportedIndexProfile) {
			if err != nil {
				return err
			}
			return application.ErrInvalidEvent
		}
		var err error
		manifestPayload, err = encodeManifest(event.Manifest)
		if err != nil {
			return application.ErrInvalidEvent
		}
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = lockBookProjection(ctx, tx, event.BookID); err != nil {
		return err
	}
	command, err := tx.Exec(ctx, `INSERT INTO retrieval.manifest_facts
		(book_id,event_id,payload_digest,source_sha256,manifest_sha256,manifest_reference,manifest_payload,correlation_id,causation_id,occurred_at,failure_category,failure_recorded_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) ON CONFLICT DO NOTHING`, event.BookID, event.EventID, event.PayloadDigest[:], event.SourceSHA256[:], event.ManifestSHA256[:], event.ManifestReference, manifestPayload, event.CorrelationID, event.CausationID, event.OccurredAt, string(category), now)
	if err != nil {
		return err
	}
	if command.RowsAffected() == 0 {
		var (
			digest          []byte
			failureCategory string
		)
		if err = tx.QueryRow(ctx, `SELECT payload_digest,coalesce(failure_category,'') FROM retrieval.manifest_facts WHERE book_id=$1 OR event_id=$2`, event.BookID, event.EventID).Scan(&digest, &failureCategory); err != nil {
			return application.ErrConflictingEvent
		}
		if !equalDigest(digest, event.PayloadDigest) {
			return application.ErrConflictingEvent
		}
		if failureCategory == "" {
			return tx.Commit(ctx)
		}
	}
	metadataExists, err := metadataExists(ctx, tx, event.BookID)
	if err != nil {
		return err
	}
	if metadataExists {
		failure := storedManifestFailure{event: event, category: category, recordedAt: now}
		if err = r.materializeStoredFailedManifest(ctx, tx, failure, protoCategory); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func metadataExists(ctx context.Context, tx queryExecer, bookID string) (bool, error) {
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM retrieval.metadata_facts WHERE book_id=$1)`, bookID).Scan(&exists); err != nil {
		return false, err
	}
	return exists, nil
}

func lockBookProjection(ctx context.Context, tx queryExecer, bookID string) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, bookID); err != nil {
		return fmt.Errorf("retrieval: lock book projection: %w", err)
	}
	return nil
}

func loadStoredManifestFailure(ctx context.Context, tx queryExecer, bookID string) (*storedManifestFailure, error) {
	var failure storedManifestFailure
	var digest, sourceSHA256, manifestSHA256 []byte
	var category string
	err := tx.QueryRow(ctx, `SELECT event_id,payload_digest,source_sha256,manifest_sha256,manifest_reference,correlation_id,causation_id,occurred_at,failure_category,failure_recorded_at
		FROM retrieval.manifest_facts WHERE book_id=$1 AND failure_category IS NOT NULL`, bookID).
		Scan(&failure.event.EventID, &digest, &sourceSHA256, &manifestSHA256, &failure.event.ManifestReference, &failure.event.CorrelationID, &failure.event.CausationID, &failure.event.OccurredAt, &category, &failure.recordedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	failure.event.BookID = bookID
	failure.event.Producer = "ingestion-service"
	failure.event.SchemaVersion = "v1"
	copy(failure.event.PayloadDigest[:], digest)
	copy(failure.event.SourceSHA256[:], sourceSHA256)
	copy(failure.event.ManifestSHA256[:], manifestSHA256)
	failure.category = domain.FailureCategory(category)
	if _, ok := failureProto(failure.category); !ok {
		return nil, application.ErrConflictingEvent
	}
	return &failure, nil
}

func (r *Postgres) materializeDeferredFailedManifest(ctx context.Context, tx queryExecer, bookID string) error {
	failure, err := loadStoredManifestFailure(ctx, tx, bookID)
	if err != nil || failure == nil {
		return err
	}
	protoCategory, ok := failureProto(failure.category)
	if !ok {
		return application.ErrConflictingEvent
	}
	return r.materializeStoredFailedManifest(ctx, tx, *failure, protoCategory)
}

func (r *Postgres) materializeStoredFailedManifest(ctx context.Context, tx queryExecer, failure storedManifestFailure, protoCategory retrievalv1.BookIndexingFailureCategory) error {
	profileDigest := domain.SupportedIndexProfile().Digest
	jobID := failedManifestJobID(failure.event)
	command, err := tx.Exec(ctx, `INSERT INTO retrieval.index_jobs
		(id,book_id,source_sha256,manifest_sha256,profile_digest,state,expected_batches,failure_category,correlation_id,created_at,updated_at)
		VALUES($1,$2,$3,$4,$5,'failed',0,$6,$7,$8,$8) ON CONFLICT DO NOTHING`, jobID, failure.event.BookID, failure.event.SourceSHA256[:], failure.event.ManifestSHA256[:], profileDigest[:], string(failure.category), failure.event.CorrelationID, failure.recordedAt)
	if err != nil {
		return err
	}
	if command.RowsAffected() == 0 {
		return nil
	}
	message := &retrievalv1.BookIndexingFailedV1{EventId: jobID + ":failed", BookId: failure.event.BookID, JobId: jobID,
		SourceSha256: failure.event.SourceSHA256[:], ManifestSha256: failure.event.ManifestSHA256[:], IndexProfileDigest: profileDigest[:], FailureCategory: protoCategory,
		CorrelationId: failure.event.CorrelationID, OccurredAt: timestamppb.New(failure.recordedAt), CausationId: failure.event.EventID, Producer: "retrieval-service", SchemaVersion: "v1", IdempotencyKey: jobID + ":failed"}
	payload, marshalErr := proto.MarshalOptions{Deterministic: true}.Marshal(message)
	if marshalErr != nil {
		return marshalErr
	}
	_, err = tx.Exec(ctx, `INSERT INTO retrieval.outbox(event_id,event_type,aggregate_id,payload,occurred_at,next_attempt_at) VALUES($1,'retrieval.book.indexing-failed.v1',$2,$3,$4,$4) ON CONFLICT DO NOTHING`, message.EventId, jobID, payload, failure.recordedAt)
	return err
}

func failedManifestJobID(event application.ManifestEvent) string {
	digest := sha256.Sum256([]byte(event.BookID + ":" + event.EventID + ":" + string(event.ManifestSHA256[:])))
	return "incompatible:" + fmt.Sprintf("%x", digest[:])
}

func failureProto(category domain.FailureCategory) (retrievalv1.BookIndexingFailureCategory, bool) {
	values := map[domain.FailureCategory]retrievalv1.BookIndexingFailureCategory{
		domain.FailureManifestIntegrity:      retrievalv1.BookIndexingFailureCategory_BOOK_INDEXING_FAILURE_CATEGORY_MANIFEST_INTEGRITY,
		domain.FailureIncompatibleProfile:    retrievalv1.BookIndexingFailureCategory_BOOK_INDEXING_FAILURE_CATEGORY_INCOMPATIBLE_PROFILE,
		domain.FailureEmbeddingUnavailable:   retrievalv1.BookIndexingFailureCategory_BOOK_INDEXING_FAILURE_CATEGORY_EMBEDDING_UNAVAILABLE,
		domain.FailureVectorStoreUnavailable: retrievalv1.BookIndexingFailureCategory_BOOK_INDEXING_FAILURE_CATEGORY_VECTOR_STORE_UNAVAILABLE,
		domain.FailureResourceLimit:          retrievalv1.BookIndexingFailureCategory_BOOK_INDEXING_FAILURE_CATEGORY_RESOURCE_LIMIT_EXCEEDED,
		domain.FailureIndexingTimeout:        retrievalv1.BookIndexingFailureCategory_BOOK_INDEXING_FAILURE_CATEGORY_INDEXING_TIMEOUT,
		domain.FailureInternalIndexing:       retrievalv1.BookIndexingFailureCategory_BOOK_INDEXING_FAILURE_CATEGORY_INTERNAL_INDEXING_ERROR,
	}
	value, found := values[category]
	return value, found
}

func (r *Postgres) FilterIndexed(ctx context.Context, values []application.Evidence) ([]application.Evidence, error) {
	if len(values) == 0 {
		return values, nil
	}
	jobIDs := make([]string, 0, len(values))
	for _, value := range values {
		if value.JobID == "" {
			return nil, errors.New("evidence has no index job")
		}
		jobIDs = append(jobIDs, value.JobID)
	}
	rows, err := r.pool.Query(ctx, `SELECT id FROM retrieval.index_jobs WHERE id=ANY($1) AND state='indexed'`, jobIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	visible := make(map[string]struct{}, len(jobIDs))
	for rows.Next() {
		var jobID string
		if err = rows.Scan(&jobID); err != nil {
			return nil, err
		}
		visible[jobID] = struct{}{}
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	result := make([]application.Evidence, 0, len(values))
	for _, value := range values {
		if _, found := visible[value.JobID]; found {
			result = append(result, value)
		}
	}
	return result, nil
}

func (r *Postgres) FilterIndexedDocuments(ctx context.Context, values []application.DocumentResult) ([]application.DocumentResult, error) {
	if len(values) == 0 {
		return values, nil
	}
	jobIDs := make([]string, 0, len(values))
	for _, value := range values {
		if value.JobID == "" {
			return nil, errors.New("document has no index job")
		}
		jobIDs = append(jobIDs, value.JobID)
	}
	rows, err := r.pool.Query(ctx, `SELECT id FROM retrieval.index_jobs WHERE id=ANY($1) AND state='indexed'`, jobIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	visible := make(map[string]struct{}, len(jobIDs))
	for rows.Next() {
		var jobID string
		if err = rows.Scan(&jobID); err != nil {
			return nil, err
		}
		visible[jobID] = struct{}{}
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	result := make([]application.DocumentResult, 0, len(values))
	for _, value := range values {
		if _, found := visible[value.JobID]; found {
			result = append(result, value)
		}
	}
	return result, nil
}

func (r *Postgres) PendingOutbox(ctx context.Context, limit int, now time.Time) ([]OutboxRecord, error) {
	if limit < 1 || limit > 100 {
		return nil, errors.New("invalid outbox limit")
	}
	rows, err := r.pool.Query(ctx, `SELECT event_id,event_type,payload FROM retrieval.outbox WHERE published_at IS NULL AND next_attempt_at <= $1 ORDER BY occurred_at LIMIT $2`, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]OutboxRecord, 0, limit)
	for rows.Next() {
		var record OutboxRecord
		if err = rows.Scan(&record.EventID, &record.EventType, &record.Payload); err != nil {
			return nil, err
		}
		result = append(result, record)
	}
	return result, rows.Err()
}

func (r *Postgres) MarkPublished(ctx context.Context, eventID string, now time.Time) error {
	_, err := r.pool.Exec(ctx, `UPDATE retrieval.outbox SET published_at=$2 WHERE event_id=$1 AND published_at IS NULL`, eventID, now)
	return err
}

func (r *Postgres) DeferOutbox(ctx context.Context, eventID string, now time.Time) error {
	_, err := r.pool.Exec(ctx, `UPDATE retrieval.outbox SET attempts=attempts+1,next_attempt_at=$2 + make_interval(secs => LEAST(300, attempts*attempts+1)) WHERE event_id=$1 AND published_at IS NULL`, eventID, now)
	return err
}

func (r *Postgres) RecoverStaleBatches(ctx context.Context, cutoff, now time.Time) (int64, error) {
	var recovered int64
	err := r.pool.QueryRow(ctx, `WITH stale AS (
		UPDATE retrieval.index_batches SET state='pending',lease_owner=NULL,lease_expires_at=NULL,next_attempt_at=$2,updated_at=$2
		WHERE state='processing' AND updated_at < $1 RETURNING id
	), replay AS (
		UPDATE retrieval.outbox o SET published_at=NULL,next_attempt_at=$2
		FROM stale WHERE o.event_id=stale.id || ':requested'
	) SELECT count(*) FROM stale`, cutoff, now).Scan(&recovered)
	if err != nil {
		return 0, err
	}
	return recovered, nil
}

func (r *Postgres) PendingVectorCleanup(ctx context.Context, limit int, now time.Time) ([]VectorCleanupJob, error) {
	if limit < 1 || limit > 256 {
		return nil, application.ErrInvalidEvent
	}
	rows, err := r.pool.Query(ctx, `SELECT id,book_id
		FROM retrieval.index_jobs
		WHERE state='failed' AND vector_cleanup_pending AND vector_cleanup_next_attempt_at <= $1
		ORDER BY vector_cleanup_next_attempt_at, updated_at
		LIMIT $2`, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := make([]VectorCleanupJob, 0, limit)
	for rows.Next() {
		var job VectorCleanupJob
		if err = rows.Scan(&job.JobID, &job.BookID); err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

func (r *Postgres) CompleteVectorCleanup(ctx context.Context, jobID string) error {
	if jobID == "" {
		return application.ErrInvalidEvent
	}
	_, err := r.pool.Exec(ctx, `UPDATE retrieval.index_jobs
		SET vector_cleanup_pending=false,vector_cleanup_attempts=0,vector_cleanup_next_attempt_at=NULL
		WHERE id=$1`, jobID)
	return err
}

func (r *Postgres) RetryVectorCleanup(ctx context.Context, jobID string, now time.Time) error {
	if jobID == "" {
		return application.ErrInvalidEvent
	}
	_, err := r.pool.Exec(ctx, `UPDATE retrieval.index_jobs
		SET vector_cleanup_attempts=vector_cleanup_attempts+1,
		    vector_cleanup_next_attempt_at=$2 + make_interval(secs => LEAST(300, vector_cleanup_attempts*vector_cleanup_attempts+1)),
		    updated_at=$2
		WHERE id=$1 AND vector_cleanup_pending`, jobID, now)
	return err
}
