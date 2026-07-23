//go:build integration

package repository

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	retrievalv1 "github.com/belLena81/raglibrarian/pkg/proto/retrieval/v1"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/application"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/domain"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/proto"
)

func TestReplayRecoveryTerminalFailureAndVisibilityUseDurableState(t *testing.T) {
	if os.Getenv("RETRIEVAL_POSTGRES_INTEGRATION") != "true" {
		t.Skip("set RETRIEVAL_POSTGRES_INTEGRATION=true against an isolated migrated database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, readIntegrationDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	repository := NewPostgres(pool)
	suffix := randomIntegrationID(t)
	bookID, jobID, batchID := "book-"+suffix, "job-"+suffix, "batch-"+suffix
	now := time.Now().UTC().Truncate(time.Microsecond)
	work := application.BatchWork{EventID: "event-" + suffix, JobID: jobID, BatchID: batchID, BookID: bookID,
		ShardReference: "books/" + bookID + "/source/profile/shards/000000.pb.zst", ShardSHA256: integrationDigest(1), SourceSHA256: integrationDigest(2),
		ManifestSHA256: integrationDigest(3), ProfileDigest: domain.SupportedIndexProfile().Digest, CompressedBytes: 10, UncompressedBytes: 20, ChunkCount: 1,
		ManifestPageCount: 1, FirstChunkOrder: 0, LastChunkOrder: 0, ExtractionVersion: domain.SupportedIndexProfile().ExtractionVersion,
		NormalizationVersion: domain.SupportedIndexProfile().NormalizationVersion, TokenizerVersion: domain.SupportedIndexProfile().TokenizerVersion,
		ChunkingVersion: domain.SupportedIndexProfile().ChunkingVersion, StructureVersion: domain.SupportedIndexProfile().StructureVersion,
		MaximumTokens: uint32(domain.SupportedIndexProfile().MaximumTokens), OverlapTokens: uint32(domain.SupportedIndexProfile().OverlapTokens),
		CorrelationID: "correlation-" + suffix, CausationID: "cause-" + suffix, Producer: "retrieval-service", SchemaVersion: "v1", IdempotencyKey: batchID, OccurredAt: now}
	payloadDigest := integrationDigest(4)

	_, err = pool.Exec(ctx, `INSERT INTO retrieval.metadata_facts(book_id,event_id,payload_digest,source_sha256,title,author,publication_year,tags,correlation_id,causation_id,occurred_at)
		VALUES($1,$2,$3,$4,'Synthetic systems','RAGLibrarian QA',2026,'{}',$5,$6,$7)`, bookID, "metadata-"+suffix, payloadDigest[:], work.SourceSHA256[:], work.CorrelationID, work.CausationID, now)
	if err != nil {
		t.Fatal(err)
	}
	insertManifestFact(t, ctx, pool, bookID, work.SourceSHA256, work.ManifestSHA256, "correlation-"+suffix, "cause-"+suffix, now, manifestForWork(work))
	_, err = pool.Exec(ctx, `INSERT INTO retrieval.index_jobs(id,book_id,source_sha256,manifest_sha256,profile_digest,state,expected_batches,correlation_id,created_at,updated_at)
		VALUES($1,$2,$3,$4,$5,'pending',1,$6,$7,$7)`, jobID, bookID, work.SourceSHA256[:], work.ManifestSHA256[:], work.ProfileDigest[:], work.CorrelationID, now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO retrieval.index_batches(id,job_id,shard_reference,shard_sha256,compressed_byte_size,uncompressed_byte_size,chunk_count,updated_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, batchID, jobID, work.ShardReference, work.ShardSHA256[:], work.CompressedBytes, work.UncompressedBytes, work.ChunkCount, now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO retrieval.outbox(event_id,event_type,aggregate_id,payload,occurred_at,published_at,next_attempt_at)
		VALUES($1,'retrieval.index-batch.requested.v1',$2,'x',$3,$3,$3)`, batchID+":requested", jobID, now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM retrieval.outbox WHERE aggregate_id=$1`, jobID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM retrieval.index_jobs WHERE id=$1`, jobID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM retrieval.manifest_facts WHERE book_id=$1`, bookID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM retrieval.metadata_facts WHERE book_id=$1`, bookID)
	})

	if _, accepted, beginErr := repository.BeginBatch(ctx, work); beginErr != nil || !accepted {
		t.Fatalf("BeginBatch() accepted=%v error=%v", accepted, beginErr)
	}
	_, err = pool.Exec(ctx, `UPDATE retrieval.index_batches SET updated_at=$2 WHERE id=$1`, batchID, now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if recovered, recoverErr := repository.RecoverStaleBatches(ctx, now.Add(-30*time.Minute), now); recoverErr != nil || recovered != 1 {
		t.Fatalf("RecoverStaleBatches() recovered=%d error=%v", recovered, recoverErr)
	}
	if _, accepted, beginErr := repository.BeginBatch(ctx, work); beginErr != nil || !accepted {
		t.Fatalf("replayed BeginBatch() accepted=%v error=%v", accepted, beginErr)
	}
	if transitioned, failErr := repository.FailBatch(ctx, work, domain.FailureInternalIndexing, now); failErr != nil || !transitioned {
		t.Fatalf("FailBatch() transitioned=%v error=%v", transitioned, failErr)
	}
	if transitioned, failErr := repository.FailBatch(ctx, work, domain.FailureInternalIndexing, now.Add(time.Second)); failErr != nil || transitioned {
		t.Fatalf("replayed FailBatch() transitioned=%v error=%v", transitioned, failErr)
	}
	pendingJobs, pendingErr := repository.PendingVectorCleanup(ctx, 8, now)
	if pendingErr != nil || len(pendingJobs) != 1 || pendingJobs[0].JobID != jobID {
		t.Fatalf("PendingVectorCleanup() jobs=%#v error=%v", pendingJobs, pendingErr)
	}
	if err = repository.RetryVectorCleanup(ctx, jobID, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err = repository.CompleteVectorCleanup(ctx, jobID); err != nil {
		t.Fatal(err)
	}
	pendingJobs, pendingErr = repository.PendingVectorCleanup(ctx, 8, now.Add(5*time.Minute))
	if pendingErr != nil || len(pendingJobs) != 0 {
		t.Fatalf("post-complete PendingVectorCleanup() jobs=%#v error=%v", pendingJobs, pendingErr)
	}
	var terminalEvents int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM retrieval.outbox WHERE event_id=$1`, jobID+":failed").Scan(&terminalEvents); err != nil || terminalEvents != 1 {
		t.Fatalf("terminal event count=%d error=%v", terminalEvents, err)
	}
	visible, err := repository.FilterIndexed(ctx, []application.Evidence{{EvidenceID: "hidden", JobID: jobID, BookID: bookID, Passage: "must remain hidden"}})
	if err != nil || len(visible) != 0 {
		t.Fatalf("failed job visibility=%#v error=%v", visible, err)
	}
}

func TestBeginBatchRejectsTamperedManifestBounds(t *testing.T) {
	if os.Getenv("RETRIEVAL_POSTGRES_INTEGRATION") != "true" {
		t.Skip("set RETRIEVAL_POSTGRES_INTEGRATION=true against an isolated migrated database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, readIntegrationDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	repository := NewPostgres(pool)
	suffix := randomIntegrationID(t)
	bookID, jobID, batchID := "book-"+suffix, "job-"+suffix, "batch-"+suffix
	now := time.Now().UTC().Truncate(time.Microsecond)
	work := validIntegrationBatchWork(bookID, jobID, batchID, now)
	payloadDigest := integrationDigest(4)

	_, err = pool.Exec(ctx, `INSERT INTO retrieval.metadata_facts(book_id,event_id,payload_digest,source_sha256,title,author,publication_year,tags,correlation_id,causation_id,occurred_at)
		VALUES($1,$2,$3,$4,'Synthetic systems','RAGLibrarian QA',2026,'{}',$5,$6,$7)`, bookID, "metadata-"+suffix, payloadDigest[:], work.SourceSHA256[:], work.CorrelationID, work.CausationID, now)
	if err != nil {
		t.Fatal(err)
	}
	insertManifestFact(t, ctx, pool, bookID, work.SourceSHA256, work.ManifestSHA256, work.CorrelationID, work.CausationID, now, manifestForWork(work))
	_, err = pool.Exec(ctx, `INSERT INTO retrieval.index_jobs(id,book_id,source_sha256,manifest_sha256,profile_digest,state,expected_batches,correlation_id,created_at,updated_at)
		VALUES($1,$2,$3,$4,$5,'pending',1,$6,$7,$7)`, jobID, bookID, work.SourceSHA256[:], work.ManifestSHA256[:], work.ProfileDigest[:], work.CorrelationID, now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO retrieval.index_batches(id,job_id,shard_reference,shard_sha256,compressed_byte_size,uncompressed_byte_size,chunk_count,updated_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, batchID, jobID, work.ShardReference, work.ShardSHA256[:], work.CompressedBytes, work.UncompressedBytes, work.ChunkCount, now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM retrieval.outbox WHERE aggregate_id=$1`, jobID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM retrieval.index_batches WHERE job_id=$1`, jobID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM retrieval.index_jobs WHERE id=$1`, jobID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM retrieval.manifest_facts WHERE book_id=$1`, bookID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM retrieval.metadata_facts WHERE book_id=$1`, bookID)
	})

	t.Run("tampered page count", func(t *testing.T) {
		tampered := work
		tampered.ManifestPageCount++
		_, accepted, beginErr := repository.BeginBatch(ctx, tampered)
		if accepted || !errors.Is(beginErr, application.ErrConflictingEvent) {
			t.Fatalf("BeginBatch() accepted=%v error=%v", accepted, beginErr)
		}
	})

	t.Run("tampered order bounds", func(t *testing.T) {
		tampered := work
		tampered.LastChunkOrder++
		_, accepted, beginErr := repository.BeginBatch(ctx, tampered)
		if accepted || !errors.Is(beginErr, application.ErrConflictingEvent) {
			t.Fatalf("BeginBatch() accepted=%v error=%v", accepted, beginErr)
		}
	})
}

func TestFailManifestEmitsIncompatibleProfileTerminalEvent(t *testing.T) {
	if os.Getenv("RETRIEVAL_POSTGRES_INTEGRATION") != "true" {
		t.Skip("set RETRIEVAL_POSTGRES_INTEGRATION=true against an isolated migrated database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, readIntegrationDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	repository := NewPostgres(pool)
	suffix := randomIntegrationID(t)
	bookID := "book-" + suffix
	now := time.Now().UTC().Truncate(time.Microsecond)
	sourceSHA256 := integrationDigest(1)
	manifestSHA256 := integrationDigest(2)
	processingDigest := integrationDigest(7)
	processingDigestHex := hex.EncodeToString(processingDigest[:])
	prefix := "books/" + bookID + "/" + hex.EncodeToString(sourceSHA256[:]) + "/" + processingDigestHex + "/"
	payloadDigest := integrationDigest(3)
	_, err = pool.Exec(ctx, `INSERT INTO retrieval.metadata_facts(book_id,event_id,payload_digest,source_sha256,title,author,publication_year,tags,correlation_id,causation_id,occurred_at)
		VALUES($1,$2,$3,$4,'Synthetic systems','RAGLibrarian QA',2026,'{}',$5,$6,$7)`, bookID, "metadata-"+suffix, payloadDigest[:], sourceSHA256[:], "correlation-"+suffix, "cause-"+suffix, now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM retrieval.outbox WHERE aggregate_id LIKE 'incompatible:%'`)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM retrieval.index_jobs WHERE book_id=$1`, bookID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM retrieval.manifest_facts WHERE book_id=$1`, bookID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM retrieval.metadata_facts WHERE book_id=$1`, bookID)
	})
	event := application.ManifestEvent{EventID: "manifest-" + suffix, BookID: bookID, SourceSHA256: sourceSHA256, ManifestSHA256: manifestSHA256,
		ManifestReference: prefix + "manifest.pb", CorrelationID: "correlation-" + suffix, CausationID: "cause-" + suffix,
		Producer: "ingestion-service", SchemaVersion: "v1", IdempotencyKey: bookID + ":" + processingDigestHex + ":ready", OccurredAt: now, PayloadDigest: integrationDigest(4),
		Manifest: application.Manifest{SchemaVersion: "v1", BookID: bookID, SourceSHA256: sourceSHA256, ManifestSHA256: manifestSHA256,
			ProcessingConfigDigest: processingDigest, PageCount: 1, ChunkCount: 1, GeneratedAt: now.Add(-time.Minute),
			ExtractionVersion: "poppler-layout-v1", NormalizationVersion: "nfc-v1", TokenizerVersion: "cl100k_base-v1",
			ChunkingVersion: "token-window-v1", StructureVersion: "heading-carry-v1", MaximumTokens: 800, OverlapTokens: 120,
			Shards: []application.Shard{{Reference: prefix + "shards/000000.pb.zst", SHA256: integrationDigest(5), CompressedBytes: 10, UncompressedBytes: 20, ChunkCount: 1, FirstChunkOrder: 0, LastChunkOrder: 0}}}}

	if err = repository.FailManifest(ctx, event, domain.FailureIncompatibleProfile, now); err != nil {
		t.Fatal(err)
	}
	if err = repository.FailManifest(ctx, event, domain.FailureIncompatibleProfile, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}

	var state, category string
	var outboxPayload []byte
	if err = pool.QueryRow(ctx, `SELECT state,failure_category FROM retrieval.index_jobs WHERE book_id=$1`, bookID).Scan(&state, &category); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT payload FROM retrieval.outbox WHERE event_type='retrieval.book.indexing-failed.v1' AND aggregate_id=(SELECT id FROM retrieval.index_jobs WHERE book_id=$1)`, bookID).Scan(&outboxPayload); err != nil {
		t.Fatal(err)
	}
	var message retrievalv1.BookIndexingFailedV1
	if err = proto.Unmarshal(outboxPayload, &message); err != nil {
		t.Fatal(err)
	}
	if state != "failed" || category != string(domain.FailureIncompatibleProfile) ||
		message.FailureCategory != retrievalv1.BookIndexingFailureCategory_BOOK_INDEXING_FAILURE_CATEGORY_INCOMPATIBLE_PROFILE {
		t.Fatalf("unexpected terminal failure: state=%q category=%q message=%s", state, category, message.FailureCategory)
	}
}

func TestFenceDeletionCreatesDurableTombstoneWhenMetadataProjectionIsMissing(t *testing.T) {
	if os.Getenv("RETRIEVAL_POSTGRES_INTEGRATION") != "true" {
		t.Skip("set RETRIEVAL_POSTGRES_INTEGRATION=true against an isolated migrated database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, readIntegrationDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	repository := NewPostgres(pool)
	suffix := randomIntegrationID(t)
	bookID := "book-" + suffix
	now := time.Now().UTC().Truncate(time.Microsecond)
	event := application.LifecycleEvent{
		EventID:          "delete-event-" + suffix,
		BookID:           bookID,
		CommandID:        "delete-command-" + suffix,
		ActorID:          "actor-" + suffix,
		CorrelationID:    "correlation-" + suffix,
		CausationID:      "delete-command-" + suffix,
		Producer:         "catalog-service",
		SchemaVersion:    "v1",
		IdempotencyKey:   "delete-command-" + suffix,
		Kind:             application.LifecycleDelete,
		LifecycleVersion: 2,
		PayloadDigest:    integrationDigest(81),
		OccurredAt:       now,
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM retrieval.outbox WHERE aggregate_id=$1`, bookID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM retrieval.manifest_facts WHERE book_id=$1`, bookID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM retrieval.index_jobs WHERE book_id=$1`, bookID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM retrieval.book_lifecycle WHERE book_id=$1`, bookID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM retrieval.metadata_facts WHERE book_id=$1`, bookID)
	})

	cleanupRequired, err := repository.FenceDeletion(ctx, event, now)

	if !cleanupRequired || err != nil {
		t.Fatalf("FenceDeletion() cleanup=%v error=%v, want durable cleanup", cleanupRequired, err)
	}
	if err = repository.CompleteDeletion(ctx, application.DeletionCleanup{
		BookID:           bookID,
		EventID:          event.EventID,
		CommandID:        event.CommandID,
		CorrelationID:    event.CorrelationID,
		LifecycleVersion: event.LifecycleVersion,
	}, now.Add(time.Second)); err != nil {
		t.Fatalf("CompleteDeletion() error = %v", err)
	}
	var lifecycleState string
	var ackEvents int
	if err = pool.QueryRow(ctx, `SELECT state FROM retrieval.book_lifecycle WHERE book_id=$1`, bookID).Scan(&lifecycleState); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM retrieval.outbox
		WHERE aggregate_id=$1 AND event_type='retrieval.book.index-deleted.v1'`, bookID).Scan(&ackEvents); err != nil {
		t.Fatal(err)
	}
	if lifecycleState != "deleted" || ackEvents != 1 {
		t.Fatalf("post-cleanup lifecycle state=%q ack events=%d", lifecycleState, ackEvents)
	}
	metadata := application.MetadataEvent{
		EventID:        "metadata-" + suffix,
		BookID:         bookID,
		Title:          "Must be scrubbed",
		Author:         "Must be scrubbed",
		Year:           2026,
		CorrelationID:  "correlation-" + suffix,
		CausationID:    "upload-" + suffix,
		Producer:       "catalog-service",
		SchemaVersion:  "v1",
		IdempotencyKey: bookID,
		SourceSHA256:   integrationDigest(82),
		PayloadDigest:  integrationDigest(83),
		OccurredAt:     now.Add(2 * time.Second),
	}
	if _, err = repository.ProjectMetadata(ctx, metadata); err != nil {
		t.Fatalf("ProjectMetadata() after deletion error = %v", err)
	}
	var title, author string
	var year int
	if err = pool.QueryRow(ctx, `SELECT title,author,publication_year FROM retrieval.metadata_facts WHERE book_id=$1`, bookID).Scan(&title, &author, &year); err != nil {
		t.Fatal(err)
	}
	if title != "" || author != "" || year != 0 {
		t.Fatalf("delayed metadata was not scrubbed: title=%q author=%q year=%d", title, author, year)
	}
	work := validIntegrationBatchWork(bookID, "job-"+suffix, "batch-"+suffix, now.Add(3*time.Second))
	work.SourceSHA256 = metadata.SourceSHA256
	work.ManifestSHA256 = integrationDigest(84)
	manifest := manifestForWork(work)
	manifest.SourceSHA256 = work.SourceSHA256
	manifest.ManifestSHA256 = work.ManifestSHA256
	manifestSnapshot, err := repository.ProjectManifest(ctx, application.ManifestEvent{
		EventID:           "manifest-" + suffix,
		BookID:            bookID,
		ManifestReference: "books/" + bookID + "/source/profile/manifest.pb",
		CorrelationID:     "correlation-" + suffix,
		CausationID:       "chunks-" + suffix,
		Producer:          "ingestion-service",
		SchemaVersion:     "v1",
		IdempotencyKey:    bookID + ":ready",
		SourceSHA256:      work.SourceSHA256,
		ManifestSHA256:    work.ManifestSHA256,
		PayloadDigest:     integrationDigest(85),
		OccurredAt:        now.Add(3 * time.Second),
		Manifest:          manifest,
	})
	if err != nil || !manifestSnapshot.Planned {
		t.Fatalf("ProjectManifest() after deletion snapshot=%+v error=%v, want ignored planned snapshot", manifestSnapshot, err)
	}
	var manifestRows int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM retrieval.manifest_facts WHERE book_id=$1`, bookID).Scan(&manifestRows); err != nil {
		t.Fatal(err)
	}
	if manifestRows != 0 {
		t.Fatalf("delayed manifest rows after deletion = %d, want 0", manifestRows)
	}
	accepted, err := repository.CommitPlan(ctx, application.PlanningSnapshot{
		Metadata: &metadata,
		Manifest: &application.ManifestEvent{
			BookID:         bookID,
			SourceSHA256:   metadata.SourceSHA256,
			ManifestSHA256: work.ManifestSHA256,
		},
	}, []application.BatchPlan{{
		JobID:            "job-" + suffix,
		BatchID:          "batch-" + suffix,
		BookID:           bookID,
		ProfileDigest:    domain.SupportedIndexProfile().Digest,
		OccurredAt:       now.Add(3 * time.Second),
		LifecycleVersion: 1,
	}})
	if err != nil || accepted {
		t.Fatalf("CommitPlan() after deletion accepted=%v error=%v, want ignored", accepted, err)
	}
}

func TestFailManifestIntegrityDoesNotPersistCorruptManifestPayload(t *testing.T) {
	if os.Getenv("RETRIEVAL_POSTGRES_INTEGRATION") != "true" {
		t.Skip("set RETRIEVAL_POSTGRES_INTEGRATION=true against an isolated migrated database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, readIntegrationDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	repository := NewPostgres(pool)
	suffix := randomIntegrationID(t)
	bookID := "book-" + suffix
	now := time.Now().UTC().Truncate(time.Microsecond)
	sourceSHA256 := integrationDigest(1)
	manifestSHA256 := integrationDigest(2)
	processingDigest := integrationDigest(7)
	metadataPayloadDigest := integrationDigest(3)
	processingDigestHex := hex.EncodeToString(processingDigest[:])
	prefix := "books/" + bookID + "/" + hex.EncodeToString(sourceSHA256[:]) + "/" + processingDigestHex + "/"
	_, err = pool.Exec(ctx, `INSERT INTO retrieval.metadata_facts(book_id,event_id,payload_digest,source_sha256,title,author,publication_year,tags,correlation_id,causation_id,occurred_at)
		VALUES($1,$2,$3,$4,'Synthetic systems','RAGLibrarian QA',2026,'{}',$5,$6,$7)`, bookID, "metadata-"+suffix, metadataPayloadDigest[:], sourceSHA256[:], "correlation-"+suffix, "cause-"+suffix, now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM retrieval.outbox WHERE aggregate_id IN (SELECT id FROM retrieval.index_jobs WHERE book_id=$1)`, bookID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM retrieval.index_jobs WHERE book_id=$1`, bookID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM retrieval.manifest_facts WHERE book_id=$1`, bookID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM retrieval.metadata_facts WHERE book_id=$1`, bookID)
	})
	event := application.ManifestEvent{EventID: "manifest-" + suffix, BookID: bookID, SourceSHA256: sourceSHA256, ManifestSHA256: manifestSHA256,
		ManifestReference: prefix + "manifest.pb", CorrelationID: "correlation-" + suffix, CausationID: "cause-" + suffix,
		Producer: "ingestion-service", SchemaVersion: "v1", IdempotencyKey: bookID + ":" + processingDigestHex + ":ready", OccurredAt: now, PayloadDigest: integrationDigest(4)}
	if err = repository.FailManifest(ctx, event, domain.FailureManifestIntegrity, now); err != nil {
		t.Fatal(err)
	}
	if err = repository.FailManifest(ctx, event, domain.FailureManifestIntegrity, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	var payloadBytes, terminalEvents int
	var category string
	if err = pool.QueryRow(ctx, `SELECT octet_length(manifest_payload) FROM retrieval.manifest_facts WHERE book_id=$1`, bookID).Scan(&payloadBytes); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT failure_category FROM retrieval.index_jobs WHERE book_id=$1`, bookID).Scan(&category); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM retrieval.outbox WHERE aggregate_id IN (SELECT id FROM retrieval.index_jobs WHERE book_id=$1)`, bookID).Scan(&terminalEvents); err != nil {
		t.Fatal(err)
	}
	if payloadBytes != 0 || category != string(domain.FailureManifestIntegrity) || terminalEvents != 1 {
		t.Fatalf("manifest payload bytes=%d category=%q terminal events=%d", payloadBytes, category, terminalEvents)
	}
}

func TestFailManifestDefersFailedJobUntilMetadataExists(t *testing.T) {
	if os.Getenv("RETRIEVAL_POSTGRES_INTEGRATION") != "true" {
		t.Skip("set RETRIEVAL_POSTGRES_INTEGRATION=true against an isolated migrated database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, readIntegrationDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	repository := NewPostgres(pool)
	suffix := randomIntegrationID(t)
	bookID := "book-" + suffix
	now := time.Now().UTC().Truncate(time.Microsecond)
	sourceSHA256 := integrationDigest(1)
	manifestSHA256 := integrationDigest(2)
	processingDigest := integrationDigest(7)
	processingDigestHex := hex.EncodeToString(processingDigest[:])
	prefix := "books/" + bookID + "/" + hex.EncodeToString(sourceSHA256[:]) + "/" + processingDigestHex + "/"
	event := application.ManifestEvent{EventID: "manifest-" + suffix, BookID: bookID, SourceSHA256: sourceSHA256, ManifestSHA256: manifestSHA256,
		ManifestReference: prefix + "manifest.pb", CorrelationID: "correlation-" + suffix, CausationID: "cause-" + suffix,
		Producer: "ingestion-service", SchemaVersion: "v1", IdempotencyKey: bookID + ":" + processingDigestHex + ":ready", OccurredAt: now, PayloadDigest: integrationDigest(4)}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM retrieval.outbox WHERE aggregate_id LIKE 'incompatible:%'`)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM retrieval.index_jobs WHERE book_id=$1`, bookID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM retrieval.manifest_facts WHERE book_id=$1`, bookID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM retrieval.metadata_facts WHERE book_id=$1`, bookID)
	})

	if err = repository.FailManifest(ctx, event, domain.FailureManifestIntegrity, now); err != nil {
		t.Fatal(err)
	}

	var storedCategory string
	var recordedAt time.Time
	var jobs, terminalEvents int
	if err = pool.QueryRow(ctx, `SELECT failure_category,failure_recorded_at FROM retrieval.manifest_facts WHERE book_id=$1`, bookID).Scan(&storedCategory, &recordedAt); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM retrieval.index_jobs WHERE book_id=$1`, bookID).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM retrieval.outbox WHERE event_type='retrieval.book.indexing-failed.v1'`).Scan(&terminalEvents); err != nil {
		t.Fatal(err)
	}
	if storedCategory != string(domain.FailureManifestIntegrity) || !recordedAt.Equal(now) || jobs != 0 || terminalEvents != 0 {
		t.Fatalf("stored category=%q recorded_at=%s jobs=%d terminal events=%d", storedCategory, recordedAt, jobs, terminalEvents)
	}
}

func TestProjectMetadataMaterializesDeferredFailedManifestOnce(t *testing.T) {
	if os.Getenv("RETRIEVAL_POSTGRES_INTEGRATION") != "true" {
		t.Skip("set RETRIEVAL_POSTGRES_INTEGRATION=true against an isolated migrated database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, readIntegrationDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	repository := NewPostgres(pool)
	suffix := randomIntegrationID(t)
	bookID := "book-" + suffix
	now := time.Now().UTC().Truncate(time.Microsecond)
	sourceSHA256 := integrationDigest(1)
	manifestSHA256 := integrationDigest(2)
	processingDigest := integrationDigest(7)
	processingDigestHex := hex.EncodeToString(processingDigest[:])
	prefix := "books/" + bookID + "/" + hex.EncodeToString(sourceSHA256[:]) + "/" + processingDigestHex + "/"
	event := application.ManifestEvent{EventID: "manifest-" + suffix, BookID: bookID, SourceSHA256: sourceSHA256, ManifestSHA256: manifestSHA256,
		ManifestReference: prefix + "manifest.pb", CorrelationID: "correlation-" + suffix, CausationID: "cause-" + suffix,
		Producer: "ingestion-service", SchemaVersion: "v1", IdempotencyKey: bookID + ":" + processingDigestHex + ":ready", OccurredAt: now, PayloadDigest: integrationDigest(4)}
	metadata := application.MetadataEvent{EventID: "metadata-" + suffix, BookID: bookID, Title: "Synthetic systems", Author: "RAGLibrarian QA", Year: 2026,
		Tags: []string{}, SourceSHA256: sourceSHA256, PayloadDigest: integrationDigest(3), CorrelationID: "correlation-" + suffix, CausationID: "cause-" + suffix,
		Producer: "catalog-service", SchemaVersion: "v1", IdempotencyKey: bookID, OccurredAt: now.Add(time.Second)}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM retrieval.outbox WHERE aggregate_id LIKE 'incompatible:%'`)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM retrieval.index_jobs WHERE book_id=$1`, bookID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM retrieval.manifest_facts WHERE book_id=$1`, bookID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM retrieval.metadata_facts WHERE book_id=$1`, bookID)
	})

	if err = repository.FailManifest(ctx, event, domain.FailureManifestIntegrity, now); err != nil {
		t.Fatal(err)
	}
	if _, err = repository.ProjectMetadata(ctx, metadata); err != nil {
		t.Fatal(err)
	}
	if _, err = repository.ProjectMetadata(ctx, metadata); err != nil {
		t.Fatal(err)
	}

	jobID := failedManifestJobID(event)
	var state, category string
	var createdAt time.Time
	var outboxPayload []byte
	var jobs, terminalEvents int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM retrieval.index_jobs WHERE id=$1`, jobID).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT state,failure_category,created_at FROM retrieval.index_jobs WHERE id=$1`, jobID).Scan(&state, &category, &createdAt); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM retrieval.outbox WHERE event_id=$1`, jobID+":failed").Scan(&terminalEvents); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT payload FROM retrieval.outbox WHERE event_id=$1`, jobID+":failed").Scan(&outboxPayload); err != nil {
		t.Fatal(err)
	}
	var message retrievalv1.BookIndexingFailedV1
	if err = proto.Unmarshal(outboxPayload, &message); err != nil {
		t.Fatal(err)
	}
	if jobs != 1 || terminalEvents != 1 || state != "failed" || category != string(domain.FailureManifestIntegrity) || !createdAt.Equal(now) ||
		message.BookId != bookID || message.FailureCategory != retrievalv1.BookIndexingFailureCategory_BOOK_INDEXING_FAILURE_CATEGORY_MANIFEST_INTEGRITY {
		t.Fatalf("jobs=%d events=%d state=%q category=%q created_at=%s message=%s", jobs, terminalEvents, state, category, createdAt, message.FailureCategory)
	}
}

func TestProjectMetadataAndFailManifestConcurrentlyMaterializeTerminalFailure(t *testing.T) {
	if os.Getenv("RETRIEVAL_POSTGRES_INTEGRATION") != "true" {
		t.Skip("set RETRIEVAL_POSTGRES_INTEGRATION=true against an isolated migrated database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, readIntegrationDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	repository := NewPostgres(pool)
	suffix := randomIntegrationID(t)
	bookID := "book-" + suffix
	now := time.Now().UTC().Truncate(time.Microsecond)
	sourceSHA256 := integrationDigest(1)
	manifestSHA256 := integrationDigest(2)
	processingDigest := integrationDigest(7)
	processingDigestHex := hex.EncodeToString(processingDigest[:])
	prefix := "books/" + bookID + "/" + hex.EncodeToString(sourceSHA256[:]) + "/" + processingDigestHex + "/"
	event := application.ManifestEvent{
		EventID:           "manifest-" + suffix,
		BookID:            bookID,
		SourceSHA256:      sourceSHA256,
		ManifestSHA256:    manifestSHA256,
		ManifestReference: prefix + "manifest.pb",
		CorrelationID:     "correlation-" + suffix,
		CausationID:       "cause-" + suffix,
		Producer:          "ingestion-service",
		SchemaVersion:     "v1",
		IdempotencyKey:    bookID + ":" + processingDigestHex + ":ready",
		OccurredAt:        now,
		PayloadDigest:     integrationDigest(4),
	}
	metadata := application.MetadataEvent{
		EventID:        "metadata-" + suffix,
		BookID:         bookID,
		Title:          "Synthetic systems",
		Author:         "RAGLibrarian QA",
		Year:           2026,
		Tags:           []string{},
		SourceSHA256:   sourceSHA256,
		PayloadDigest:  integrationDigest(3),
		CorrelationID:  "correlation-" + suffix,
		CausationID:    "cause-" + suffix,
		Producer:       "catalog-service",
		SchemaVersion:  "v1",
		IdempotencyKey: bookID,
		OccurredAt:     now.Add(time.Second),
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM retrieval.outbox WHERE aggregate_id LIKE 'incompatible:%'`)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM retrieval.index_jobs WHERE book_id=$1`, bookID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM retrieval.manifest_facts WHERE book_id=$1`, bookID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM retrieval.metadata_facts WHERE book_id=$1`, bookID)
	})

	start := make(chan struct{})
	errCh := make(chan error, 2)
	var group sync.WaitGroup
	group.Add(2)

	go func() {
		defer group.Done()
		<-start
		_, projectErr := repository.ProjectMetadata(ctx, metadata)
		if projectErr != nil {
			errCh <- fmt.Errorf("ProjectMetadata: %w", projectErr)
			return
		}
		errCh <- nil
	}()
	go func() {
		defer group.Done()
		<-start
		failErr := repository.FailManifest(ctx, event, domain.FailureManifestIntegrity, now)
		if failErr != nil {
			errCh <- fmt.Errorf("FailManifest: %w", failErr)
			return
		}
		errCh <- nil
	}()

	close(start)
	group.Wait()
	close(errCh)

	for callErr := range errCh {
		if callErr != nil {
			t.Fatalf("concurrent projection failed: %v", callErr)
		}
	}

	jobID := failedManifestJobID(event)
	var jobs, terminalEvents int
	var state, category string
	var createdAt, recordedAt time.Time
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM retrieval.index_jobs WHERE id=$1`, jobID).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM retrieval.outbox WHERE event_id=$1`, jobID+":failed").Scan(&terminalEvents); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT state,failure_category,created_at FROM retrieval.index_jobs WHERE id=$1`, jobID).Scan(&state, &category, &createdAt); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT failure_recorded_at FROM retrieval.manifest_facts WHERE book_id=$1`, bookID).Scan(&recordedAt); err != nil {
		t.Fatal(err)
	}
	if jobs != 1 || terminalEvents != 1 || state != "failed" || category != string(domain.FailureManifestIntegrity) || !createdAt.Equal(now) || !recordedAt.Equal(now) {
		t.Fatalf("jobs=%d events=%d state=%q category=%q created_at=%s recorded_at=%s", jobs, terminalEvents, state, category, createdAt, recordedAt)
	}

	if _, err = repository.ProjectMetadata(ctx, metadata); err != nil {
		t.Fatal(err)
	}
	if err = repository.FailManifest(ctx, event, domain.FailureManifestIntegrity, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM retrieval.index_jobs WHERE id=$1`, jobID).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM retrieval.outbox WHERE event_id=$1`, jobID+":failed").Scan(&terminalEvents); err != nil {
		t.Fatal(err)
	}
	if jobs != 1 || terminalEvents != 1 {
		t.Fatalf("replay jobs=%d terminal events=%d", jobs, terminalEvents)
	}
}

func TestFailManifestReplayPreservesStoredValidManifest(t *testing.T) {
	if os.Getenv("RETRIEVAL_POSTGRES_INTEGRATION") != "true" {
		t.Skip("set RETRIEVAL_POSTGRES_INTEGRATION=true against an isolated migrated database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, readIntegrationDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	repository := NewPostgres(pool)
	suffix := randomIntegrationID(t)
	bookID := "book-" + suffix
	now := time.Now().UTC().Truncate(time.Microsecond)
	sourceSHA256 := integrationDigest(1)
	manifestSHA256 := integrationDigest(2)
	processingDigest := integrationDigest(7)
	processingDigestHex := hex.EncodeToString(processingDigest[:])
	prefix := "books/" + bookID + "/" + hex.EncodeToString(sourceSHA256[:]) + "/" + processingDigestHex + "/"
	metadata := application.MetadataEvent{EventID: "metadata-" + suffix, BookID: bookID, Title: "Synthetic systems", Author: "RAGLibrarian QA", Year: 2026,
		Tags: []string{}, SourceSHA256: sourceSHA256, PayloadDigest: integrationDigest(3), CorrelationID: "correlation-" + suffix, CausationID: "cause-" + suffix,
		Producer: "catalog-service", SchemaVersion: "v1", IdempotencyKey: bookID, OccurredAt: now}
	event := application.ManifestEvent{EventID: "manifest-" + suffix, BookID: bookID, SourceSHA256: sourceSHA256, ManifestSHA256: manifestSHA256,
		ManifestReference: prefix + "manifest.pb", CorrelationID: "correlation-" + suffix, CausationID: "cause-" + suffix,
		Producer: "ingestion-service", SchemaVersion: "v1", IdempotencyKey: bookID + ":" + processingDigestHex + ":ready", OccurredAt: now.Add(time.Second), PayloadDigest: integrationDigest(4),
		Manifest: application.Manifest{SchemaVersion: "v1", BookID: bookID, SourceSHA256: sourceSHA256, ManifestSHA256: manifestSHA256,
			ProcessingConfigDigest: processingDigest, PageCount: 1, ChunkCount: 1, GeneratedAt: now.Add(-time.Minute),
			ExtractionVersion: "poppler-layout-v1", NormalizationVersion: "nfc-v1", TokenizerVersion: "cl100k_base-v1",
			ChunkingVersion: "token-window-v1", StructureVersion: "heading-carry-v1", MaximumTokens: 800, OverlapTokens: 120,
			Shards: []application.Shard{{Reference: prefix + "shards/000000.pb.zst", SHA256: integrationDigest(5), CompressedBytes: 10, UncompressedBytes: 20, ChunkCount: 1, FirstChunkOrder: 0, LastChunkOrder: 0}}}}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM retrieval.outbox WHERE aggregate_id IN (SELECT id FROM retrieval.index_jobs WHERE book_id=$1)`, bookID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM retrieval.index_batches WHERE job_id IN (SELECT id FROM retrieval.index_jobs WHERE book_id=$1)`, bookID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM retrieval.index_jobs WHERE book_id=$1`, bookID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM retrieval.manifest_facts WHERE book_id=$1`, bookID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM retrieval.metadata_facts WHERE book_id=$1`, bookID)
	})

	if _, err = repository.ProjectMetadata(ctx, metadata); err != nil {
		t.Fatal(err)
	}
	snapshot, err := repository.ProjectManifest(ctx, event)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Manifest == nil || snapshot.Planned {
		t.Fatalf("snapshot manifest=%v planned=%v", snapshot.Manifest != nil, snapshot.Planned)
	}
	if err = repository.FailManifest(ctx, application.ManifestEvent{
		EventID:           event.EventID,
		BookID:            event.BookID,
		SourceSHA256:      event.SourceSHA256,
		ManifestSHA256:    event.ManifestSHA256,
		ManifestReference: event.ManifestReference,
		CorrelationID:     event.CorrelationID,
		CausationID:       event.CausationID,
		Producer:          event.Producer,
		SchemaVersion:     event.SchemaVersion,
		IdempotencyKey:    event.IdempotencyKey,
		OccurredAt:        event.OccurredAt,
		PayloadDigest:     event.PayloadDigest,
	}, domain.FailureManifestIntegrity, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}

	var failureCategory string
	var terminalEvents int
	if err = pool.QueryRow(ctx, `SELECT coalesce(failure_category,'') FROM retrieval.manifest_facts WHERE book_id=$1`, bookID).Scan(&failureCategory); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM retrieval.index_jobs WHERE book_id=$1`, bookID).Scan(&terminalEvents); err != nil {
		t.Fatal(err)
	}
	if failureCategory != "" || terminalEvents != 0 {
		t.Fatalf("failure_category=%q index_jobs=%d", failureCategory, terminalEvents)
	}

	batch := application.BatchPlan{
		JobID:             "job-" + suffix,
		BatchID:           "job-" + suffix + ":0",
		BookID:            bookID,
		Reference:         event.Manifest.Shards[0].Reference,
		SHA256:            event.Manifest.Shards[0].SHA256,
		CompressedBytes:   event.Manifest.Shards[0].CompressedBytes,
		UncompressedBytes: event.Manifest.Shards[0].UncompressedBytes,
		ChunkCount:        event.Manifest.Shards[0].ChunkCount,
		ProfileDigest:     domain.SupportedIndexProfile().Digest,
		OccurredAt:        now.Add(3 * time.Second),
	}
	committed, err := repository.CommitPlan(ctx, snapshot, []application.BatchPlan{batch})
	if err != nil || !committed {
		t.Fatalf("CommitPlan() committed=%v error=%v", committed, err)
	}
}

func TestFailBatchReturnsFalseAfterJobAlreadyIndexed(t *testing.T) {
	if os.Getenv("RETRIEVAL_POSTGRES_INTEGRATION") != "true" {
		t.Skip("set RETRIEVAL_POSTGRES_INTEGRATION=true against an isolated migrated database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, readIntegrationDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	repository := NewPostgres(pool)
	suffix := randomIntegrationID(t)
	bookID, jobID, batchID := "book-"+suffix, "job-"+suffix, "batch-"+suffix
	now := time.Now().UTC().Truncate(time.Microsecond)
	work := application.BatchWork{EventID: "event-" + suffix, JobID: jobID, BatchID: batchID, BookID: bookID,
		ShardReference: "books/" + bookID + "/source/profile/shards/000000.pb.zst", ShardSHA256: integrationDigest(1), SourceSHA256: integrationDigest(2),
		ManifestSHA256: integrationDigest(3), ProfileDigest: domain.SupportedIndexProfile().Digest, CompressedBytes: 10, UncompressedBytes: 20, ChunkCount: 1,
		CorrelationID: "correlation-" + suffix, CausationID: "cause-" + suffix, Producer: "retrieval-service", SchemaVersion: "v1", IdempotencyKey: batchID, OccurredAt: now}
	payloadDigest := integrationDigest(4)
	_, err = pool.Exec(ctx, `INSERT INTO retrieval.metadata_facts(book_id,event_id,payload_digest,source_sha256,title,author,publication_year,tags,correlation_id,causation_id,occurred_at)
		VALUES($1,$2,$3,$4,'Synthetic systems','RAGLibrarian QA',2026,'{}',$5,$6,$7)`, bookID, "metadata-"+suffix, payloadDigest[:], work.SourceSHA256[:], work.CorrelationID, work.CausationID, now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO retrieval.index_jobs(id,book_id,source_sha256,manifest_sha256,profile_digest,state,expected_batches,evidence_count,correlation_id,created_at,updated_at)
		VALUES($1,$2,$3,$4,$5,'indexed',1,1,$6,$7,$7)`, jobID, bookID, work.SourceSHA256[:], work.ManifestSHA256[:], work.ProfileDigest[:], work.CorrelationID, now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO retrieval.index_batches(id,job_id,shard_reference,shard_sha256,compressed_byte_size,uncompressed_byte_size,chunk_count,state,updated_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,'completed',$8)`, batchID, jobID, work.ShardReference, work.ShardSHA256[:], work.CompressedBytes, work.UncompressedBytes, work.ChunkCount, now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM retrieval.outbox WHERE aggregate_id=$1`, jobID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM retrieval.index_batches WHERE job_id=$1`, jobID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM retrieval.index_jobs WHERE id=$1`, jobID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM retrieval.metadata_facts WHERE book_id=$1`, bookID)
	})

	transitioned, err := repository.FailBatch(ctx, work, domain.FailureInternalIndexing, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if transitioned {
		t.Fatal("FailBatch() transitioned = true, want false")
	}

	var state string
	var cleanupPending bool
	var terminalEvents int
	if err = pool.QueryRow(ctx, `SELECT state,vector_cleanup_pending FROM retrieval.index_jobs WHERE id=$1`, jobID).Scan(&state, &cleanupPending); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM retrieval.outbox WHERE event_id=$1`, jobID+":failed").Scan(&terminalEvents); err != nil {
		t.Fatal(err)
	}
	if state != "indexed" || cleanupPending || terminalEvents != 0 {
		t.Fatalf("state=%q cleanup_pending=%v terminal_events=%d", state, cleanupPending, terminalEvents)
	}
}

func TestCompleteBatchRejectsDuplicateChunkID(t *testing.T) {
	if os.Getenv("RETRIEVAL_POSTGRES_INTEGRATION") != "true" {
		t.Skip("set RETRIEVAL_POSTGRES_INTEGRATION=true against an isolated migrated database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, readIntegrationDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	repository := NewPostgres(pool)
	suffix := randomIntegrationID(t)
	bookID, jobID, batchID := "book-"+suffix, "job-"+suffix, "batch-"+suffix
	now := time.Now().UTC().Truncate(time.Microsecond)
	work := application.BatchWork{EventID: "event-" + suffix, JobID: jobID, BatchID: batchID, BookID: bookID,
		ShardReference: "books/" + bookID + "/source/profile/shards/000000.pb.zst", ShardSHA256: integrationDigest(1), SourceSHA256: integrationDigest(2),
		ManifestSHA256: integrationDigest(3), ProfileDigest: domain.SupportedIndexProfile().Digest, CompressedBytes: 10, UncompressedBytes: 20, ChunkCount: 2,
		CorrelationID: "correlation-" + suffix, CausationID: "cause-" + suffix, Producer: "retrieval-service", SchemaVersion: "v1", IdempotencyKey: batchID, OccurredAt: now}
	payloadDigest := integrationDigest(4)
	_, err = pool.Exec(ctx, `INSERT INTO retrieval.metadata_facts(book_id,event_id,payload_digest,source_sha256,title,author,publication_year,tags,correlation_id,causation_id,occurred_at)
		VALUES($1,$2,$3,$4,'Synthetic systems','RAGLibrarian QA',2026,'{}',$5,$6,$7)`, bookID, "metadata-"+suffix, payloadDigest[:], work.SourceSHA256[:], work.CorrelationID, work.CausationID, now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO retrieval.index_jobs(id,book_id,source_sha256,manifest_sha256,profile_digest,state,expected_batches,correlation_id,created_at,updated_at)
		VALUES($1,$2,$3,$4,$5,'pending',1,$6,$7,$7)`, jobID, bookID, work.SourceSHA256[:], work.ManifestSHA256[:], work.ProfileDigest[:], work.CorrelationID, now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO retrieval.index_batches(id,job_id,shard_reference,shard_sha256,compressed_byte_size,uncompressed_byte_size,chunk_count,state,updated_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,'processing',$8)`, batchID, jobID, work.ShardReference, work.ShardSHA256[:], work.CompressedBytes, work.UncompressedBytes, work.ChunkCount, now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM retrieval.outbox WHERE aggregate_id=$1`, jobID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM retrieval.index_jobs WHERE id=$1`, jobID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM retrieval.metadata_facts WHERE book_id=$1`, bookID)
	})
	vector := make([]float32, domain.EmbeddingDimensions)
	records := []application.EvidenceRecord{
		{Evidence: application.Evidence{EvidenceID: "evidence-a-" + suffix, ChunkID: "chunk-1", JobID: jobID, BookID: bookID, Title: "Synthetic systems", Author: "RAGLibrarian QA", Passage: "first"}, JobID: jobID, ContentSHA256: integrationDigest(11), Vector: vector},
		{Evidence: application.Evidence{EvidenceID: "evidence-b-" + suffix, ChunkID: "chunk-1", JobID: jobID, BookID: bookID, Title: "Synthetic systems", Author: "RAGLibrarian QA", Passage: "second"}, JobID: jobID, ContentSHA256: integrationDigest(11), Vector: vector},
	}

	complete, err := repository.CompleteBatch(ctx, work, records, now)

	if complete || application.FailureCategory(err) != domain.FailureManifestIntegrity {
		t.Fatalf("CompleteBatch() complete=%v error=%v category=%s", complete, err, application.FailureCategory(err))
	}
	var accumulators int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM retrieval.document_embedding_accumulators WHERE job_id=$1`, jobID).Scan(&accumulators); err != nil || accumulators != 0 {
		t.Fatalf("document accumulator count=%d error=%v", accumulators, err)
	}
}

func TestCompleteBatchPersistsNilTagsAsEmptyArray(t *testing.T) {
	if os.Getenv("RETRIEVAL_POSTGRES_INTEGRATION") != "true" {
		t.Skip("set RETRIEVAL_POSTGRES_INTEGRATION=true against an isolated migrated database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, readIntegrationDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	repository := NewPostgres(pool)
	suffix := randomIntegrationID(t)
	bookID, jobID, batchID := "book-"+suffix, "job-"+suffix, "batch-"+suffix
	now := time.Now().UTC().Truncate(time.Microsecond)
	work := application.BatchWork{EventID: "event-" + suffix, JobID: jobID, BatchID: batchID, BookID: bookID,
		ShardReference: "books/" + bookID + "/source/profile/shards/000000.pb.zst", ShardSHA256: integrationDigest(1), SourceSHA256: integrationDigest(2),
		ManifestSHA256: integrationDigest(3), ProfileDigest: domain.SupportedIndexProfile().Digest, CompressedBytes: 10, UncompressedBytes: 20, ChunkCount: 1,
		CorrelationID: "correlation-" + suffix, CausationID: "cause-" + suffix, Producer: "retrieval-service", SchemaVersion: "v1", IdempotencyKey: batchID, OccurredAt: now}
	payloadDigest := integrationDigest(4)
	_, err = pool.Exec(ctx, `INSERT INTO retrieval.metadata_facts(book_id,event_id,payload_digest,source_sha256,title,author,publication_year,tags,correlation_id,causation_id,occurred_at)
		VALUES($1,$2,$3,$4,'Synthetic systems','RAGLibrarian QA',2026,'{}',$5,$6,$7)`, bookID, "metadata-"+suffix, payloadDigest[:], work.SourceSHA256[:], work.CorrelationID, work.CausationID, now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO retrieval.index_jobs(id,book_id,source_sha256,manifest_sha256,profile_digest,state,expected_batches,correlation_id,created_at,updated_at)
		VALUES($1,$2,$3,$4,$5,'pending',1,$6,$7,$7)`, jobID, bookID, work.SourceSHA256[:], work.ManifestSHA256[:], work.ProfileDigest[:], work.CorrelationID, now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO retrieval.index_batches(id,job_id,shard_reference,shard_sha256,compressed_byte_size,uncompressed_byte_size,chunk_count,state,updated_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,'processing',$8)`, batchID, jobID, work.ShardReference, work.ShardSHA256[:], work.CompressedBytes, work.UncompressedBytes, work.ChunkCount, now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM retrieval.outbox WHERE aggregate_id=$1`, jobID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM retrieval.index_jobs WHERE id=$1`, jobID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM retrieval.metadata_facts WHERE book_id=$1`, bookID)
	})
	vector := make([]float32, domain.EmbeddingDimensions)
	record := application.EvidenceRecord{
		Evidence: application.Evidence{
			EvidenceID: "evidence-" + suffix,
			ChunkID:    "chunk-1",
			JobID:      jobID,
			BookID:     bookID,
			Title:      "Synthetic systems",
			Author:     "RAGLibrarian QA",
			Passage:    "synthetic evidence",
		},
		JobID:         jobID,
		ContentSHA256: integrationDigest(11),
		Vector:        vector,
	}

	complete, err := repository.CompleteBatch(ctx, work, []application.EvidenceRecord{record}, now)
	if err != nil || !complete {
		t.Fatalf("CompleteBatch() complete=%v error=%v", complete, err)
	}
	var evidenceTags, documentTags []string
	if err = pool.QueryRow(ctx, `SELECT tags FROM retrieval.evidence WHERE evidence_id=$1`, record.EvidenceID).Scan(&evidenceTags); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT tags FROM retrieval.documents WHERE job_id=$1`, jobID).Scan(&documentTags); err != nil {
		t.Fatal(err)
	}
	if len(evidenceTags) != 0 || len(documentTags) != 0 {
		t.Fatalf("stored tags evidence=%#v document=%#v, want empty arrays", evidenceTags, documentTags)
	}
}

func TestCompleteBatchRejectsDuplicateChunkIDAcrossBatches(t *testing.T) {
	if os.Getenv("RETRIEVAL_POSTGRES_INTEGRATION") != "true" {
		t.Skip("set RETRIEVAL_POSTGRES_INTEGRATION=true against an isolated migrated database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, readIntegrationDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	repository := NewPostgres(pool)
	suffix := randomIntegrationID(t)
	bookID, jobID := "book-"+suffix, "job-"+suffix
	now := time.Now().UTC().Truncate(time.Microsecond)
	sourceSHA256 := integrationDigest(1)
	manifestSHA256 := integrationDigest(2)
	profileDigest := domain.SupportedIndexProfile().Digest
	payloadDigest := integrationDigest(3)
	_, err = pool.Exec(ctx, `INSERT INTO retrieval.metadata_facts(book_id,event_id,payload_digest,source_sha256,title,author,publication_year,tags,correlation_id,causation_id,occurred_at)
		VALUES($1,$2,$3,$4,'Synthetic systems','RAGLibrarian QA',2026,'{}',$5,$6,$7)`, bookID, "metadata-"+suffix, payloadDigest[:], sourceSHA256[:], "correlation-"+suffix, "cause-"+suffix, now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO retrieval.index_jobs(id,book_id,source_sha256,manifest_sha256,profile_digest,state,expected_batches,correlation_id,created_at,updated_at)
		VALUES($1,$2,$3,$4,$5,'pending',2,$6,$7,$7)`, jobID, bookID, sourceSHA256[:], manifestSHA256[:], profileDigest[:], "correlation-"+suffix, now)
	if err != nil {
		t.Fatal(err)
	}
	batchIDs := []string{"batch-a-" + suffix, "batch-b-" + suffix}
	for index, batchID := range batchIDs {
		shardSHA256 := integrationDigest(byte(10 + index))
		_, err = pool.Exec(ctx, `INSERT INTO retrieval.index_batches(id,job_id,shard_reference,shard_sha256,compressed_byte_size,uncompressed_byte_size,chunk_count,state,updated_at)
			VALUES($1,$2,$3,$4,10,20,1,'processing',$5)`, batchID, jobID, "books/"+bookID+"/source/profile/shards/00000"+string(rune('0'+index))+".pb.zst", shardSHA256[:], now)
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM retrieval.outbox WHERE aggregate_id=$1`, jobID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM retrieval.index_jobs WHERE id=$1`, jobID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM retrieval.metadata_facts WHERE book_id=$1`, bookID)
	})
	vector := make([]float32, domain.EmbeddingDimensions)
	firstWork := application.BatchWork{EventID: "event-" + batchIDs[0], JobID: jobID, BatchID: batchIDs[0], BookID: bookID,
		ShardReference: "books/" + bookID + "/source/profile/shards/000000.pb.zst", ShardSHA256: integrationDigest(10), SourceSHA256: sourceSHA256,
		ManifestSHA256: manifestSHA256, ProfileDigest: profileDigest, CompressedBytes: 10, UncompressedBytes: 20, ChunkCount: 1,
		CorrelationID: "correlation-" + suffix, CausationID: "cause-" + suffix, Producer: "retrieval-service", SchemaVersion: "v1", IdempotencyKey: batchIDs[0], OccurredAt: now}
	secondWork := firstWork
	secondWork.EventID = "event-" + batchIDs[1]
	secondWork.BatchID = batchIDs[1]
	secondWork.ShardReference = "books/" + bookID + "/source/profile/shards/000001.pb.zst"
	secondWork.ShardSHA256 = integrationDigest(11)
	secondWork.IdempotencyKey = batchIDs[1]
	record := application.EvidenceRecord{
		Evidence: application.Evidence{
			EvidenceID: "evidence-a-" + suffix,
			ChunkID:    "chunk-1",
			JobID:      jobID,
			BookID:     bookID,
			Title:      "Synthetic systems",
			Author:     "RAGLibrarian QA",
			Passage:    "synthetic evidence",
		},
		JobID:         jobID,
		ContentSHA256: integrationDigest(20),
		Vector:        vector,
	}

	complete, err := repository.CompleteBatch(ctx, firstWork, []application.EvidenceRecord{record}, now)
	if err != nil || complete {
		t.Fatalf("first CompleteBatch() complete=%v error=%v", complete, err)
	}
	record.EvidenceID = "evidence-b-" + suffix
	complete, err = repository.CompleteBatch(ctx, secondWork, []application.EvidenceRecord{record}, now)

	if complete || application.FailureCategory(err) != domain.FailureManifestIntegrity {
		t.Fatalf("second CompleteBatch() complete=%v error=%v category=%s", complete, err, application.FailureCategory(err))
	}
	var accumulatorChunks, documentChunks int
	if err = pool.QueryRow(ctx, `SELECT coalesce(sum(chunk_count),0) FROM retrieval.document_embedding_accumulators WHERE job_id=$1`, jobID).Scan(&accumulatorChunks); err != nil || accumulatorChunks != 1 {
		t.Fatalf("document accumulator chunks=%d error=%v", accumulatorChunks, err)
	}
	if err = pool.QueryRow(ctx, `SELECT coalesce(sum(chunk_count),0) FROM retrieval.documents WHERE job_id=$1`, jobID).Scan(&documentChunks); err != nil || documentChunks != 1 {
		t.Fatalf("document chunks=%d error=%v", documentChunks, err)
	}
}

func TestCompleteBatchSerializesFinalBatchCompletion(t *testing.T) {
	if os.Getenv("RETRIEVAL_POSTGRES_INTEGRATION") != "true" {
		t.Skip("set RETRIEVAL_POSTGRES_INTEGRATION=true against an isolated migrated database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, readIntegrationDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	repository := NewPostgres(pool)
	suffix := randomIntegrationID(t)
	bookID, jobID := "book-"+suffix, "job-"+suffix
	now := time.Now().UTC().Truncate(time.Microsecond)
	sourceSHA256 := integrationDigest(1)
	manifestSHA256 := integrationDigest(2)
	profileDigest := domain.SupportedIndexProfile().Digest
	payloadDigest := integrationDigest(3)
	_, err = pool.Exec(ctx, `INSERT INTO retrieval.metadata_facts(book_id,event_id,payload_digest,source_sha256,title,author,publication_year,tags,correlation_id,causation_id,occurred_at)
		VALUES($1,$2,$3,$4,'Synthetic systems','RAGLibrarian QA',2026,'{}',$5,$6,$7)`, bookID, "metadata-"+suffix, payloadDigest[:], sourceSHA256[:], "correlation-"+suffix, "cause-"+suffix, now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO retrieval.index_jobs(id,book_id,source_sha256,manifest_sha256,profile_digest,state,expected_batches,correlation_id,created_at,updated_at)
		VALUES($1,$2,$3,$4,$5,'pending',2,$6,$7,$7)`, jobID, bookID, sourceSHA256[:], manifestSHA256[:], profileDigest[:], "correlation-"+suffix, now)
	if err != nil {
		t.Fatal(err)
	}
	batchIDs := []string{"batch-a-" + suffix, "batch-b-" + suffix}
	for index, batchID := range batchIDs {
		shardSHA256 := integrationDigest(byte(10 + index))
		_, err = pool.Exec(ctx, `INSERT INTO retrieval.index_batches(id,job_id,shard_reference,shard_sha256,compressed_byte_size,uncompressed_byte_size,chunk_count,state,updated_at)
			VALUES($1,$2,$3,$4,10,20,1,'processing',$5)`, batchID, jobID, "books/"+bookID+"/source/profile/shards/00000"+string(rune('0'+index))+".pb.zst", shardSHA256[:], now)
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM retrieval.outbox WHERE aggregate_id=$1`, jobID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM retrieval.index_jobs WHERE id=$1`, jobID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM retrieval.metadata_facts WHERE book_id=$1`, bookID)
	})

	works := make([]application.BatchWork, len(batchIDs))
	vector := make([]float32, domain.EmbeddingDimensions)
	for index, batchID := range batchIDs {
		works[index] = application.BatchWork{
			EventID:           "event-" + batchID,
			JobID:             jobID,
			BatchID:           batchID,
			BookID:            bookID,
			ShardReference:    "books/" + bookID + "/source/profile/shards/00000" + string(rune('0'+index)) + ".pb.zst",
			ShardSHA256:       integrationDigest(byte(10 + index)),
			SourceSHA256:      sourceSHA256,
			ManifestSHA256:    manifestSHA256,
			ProfileDigest:     profileDigest,
			CompressedBytes:   10,
			UncompressedBytes: 20,
			ChunkCount:        1,
			CorrelationID:     "correlation-" + suffix,
			CausationID:       "cause-" + suffix,
			Producer:          "retrieval-service",
			SchemaVersion:     "v1",
			IdempotencyKey:    batchID,
			OccurredAt:        now,
		}
	}

	completed := make(chan bool, len(works))
	errs := make(chan error, len(works))
	var waitGroup sync.WaitGroup
	for index := range works {
		work := works[index]
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			record := application.EvidenceRecord{
				Evidence: application.Evidence{
					EvidenceID: "evidence-" + work.BatchID,
					ChunkID:    "chunk-" + work.BatchID,
					JobID:      work.JobID,
					BookID:     work.BookID,
					Title:      "Synthetic systems",
					Author:     "RAGLibrarian QA",
					Passage:    "synthetic evidence",
				},
				JobID:         work.JobID,
				ContentSHA256: integrationDigest(byte(len(work.BatchID))),
				Vector:        vector,
			}
			ready, completeErr := repository.CompleteBatch(ctx, work, []application.EvidenceRecord{record}, now)
			if completeErr != nil {
				errs <- completeErr
				return
			}
			completed <- ready
		}()
	}
	waitGroup.Wait()
	close(completed)
	close(errs)
	for completeErr := range errs {
		t.Fatal(completeErr)
	}
	finalizers := 0
	for ready := range completed {
		if ready {
			finalizers++
			if err = repository.FinalizeJob(ctx, works[0], now); err != nil {
				t.Fatal(err)
			}
		}
	}
	if finalizers != 1 {
		t.Fatalf("final batch completions = %d, want 1", finalizers)
	}
	if err = repository.FinalizeJob(ctx, works[1], now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	var state string
	var terminalEvents int
	if err = pool.QueryRow(ctx, `SELECT state FROM retrieval.index_jobs WHERE id=$1`, jobID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM retrieval.outbox WHERE event_id=$1`, jobID+":indexed").Scan(&terminalEvents); err != nil {
		t.Fatal(err)
	}
	if state != "indexed" || terminalEvents != 1 {
		t.Fatalf("terminal state = %q, events = %d", state, terminalEvents)
	}
}

func TestLifecycleFinalizeReindexMarksPriorSQLGenerationForCleanup(t *testing.T) {
	if os.Getenv("RETRIEVAL_POSTGRES_INTEGRATION") != "true" {
		t.Skip("set RETRIEVAL_POSTGRES_INTEGRATION=true against an isolated migrated database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, readIntegrationDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	repository := NewPostgres(pool)
	suffix := randomIntegrationID(t)
	bookID := "book-reindex-" + suffix
	oldJobID := "job-old-" + suffix
	newJobID := "job-new-" + suffix
	batchID := "batch-new-" + suffix
	now := time.Now().UTC().Truncate(time.Microsecond)
	sourceSHA256 := integrationDigest(31)
	manifestSHA256 := integrationDigest(32)
	profileDigest := domain.SupportedIndexProfile().Digest
	payloadDigest := integrationDigest(33)
	shardSHA256 := integrationDigest(34)
	contentSHA256 := integrationDigest(35)

	_, err = pool.Exec(ctx, `INSERT INTO retrieval.metadata_facts
		(book_id,event_id,payload_digest,source_sha256,title,author,publication_year,tags,correlation_id,causation_id,occurred_at)
		VALUES($1,$2,$3,$4,'Synthetic systems','RAGLibrarian QA',2026,'{}',$5,$6,$7)`,
		bookID, "metadata-"+suffix, payloadDigest[:], sourceSHA256[:], "correlation-"+suffix, "cause-"+suffix, now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO retrieval.index_jobs
		(id,book_id,source_sha256,manifest_sha256,profile_digest,state,expected_batches,evidence_count,correlation_id,created_at,updated_at,lifecycle_version,finalization_inflight)
		VALUES
		($1,$3,$4,$5,$6,'indexed',1,1,$7,$8,$8,1,false),
		($2,$3,$4,$5,$6,'pending',1,0,$7,$9,$9,2,true)`,
		oldJobID, newJobID, bookID, sourceSHA256[:], manifestSHA256[:], profileDigest[:], "correlation-"+suffix, now.Add(-time.Hour), now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO retrieval.book_lifecycle
		(book_id,lifecycle_version,state,active_job_id,event_id,command_id,event_type,payload_digest,correlation_id,updated_at)
		VALUES($1,2,'reindexing',$2,$3,$4,'reindex',$5,$6,$7)`,
		bookID, oldJobID, "reindex-"+suffix, "command-"+suffix, payloadDigest[:], "correlation-"+suffix, now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO retrieval.index_batches
		(id,job_id,shard_reference,shard_sha256,compressed_byte_size,uncompressed_byte_size,chunk_count,state,updated_at)
		VALUES($1,$2,$3,$4,10,20,1,'completed',$5)`,
		batchID, newJobID, "books/"+bookID+"/profile/shards/000000.pb.zst", shardSHA256[:], now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO retrieval.evidence
		(evidence_id,chunk_id,job_id,book_id,title,author,publication_year,tags,page_start,page_end,passage,content_sha256,created_at)
		VALUES($1,'chunk-new',$2,$3,'Synthetic systems','RAGLibrarian QA',2026,'{}',1,1,'evidence',$4,$5)`,
		"evidence-"+suffix, newJobID, bookID, contentSHA256[:], now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO retrieval.outbox
		(event_id,event_type,aggregate_id,payload,occurred_at,published_at,next_attempt_at)
		VALUES
		($1,'retrieval.index-batch.requested.v1',$3,'pending',$4,NULL,$4),
		($2,'retrieval.book.indexed.v1',$3,'published',$4,$4,$4)`,
		"old-pending-"+suffix, "old-published-"+suffix, oldJobID, now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM retrieval.outbox WHERE aggregate_id=ANY($1)`, []string{oldJobID, newJobID})
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM retrieval.book_lifecycle WHERE book_id=$1`, bookID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM retrieval.index_jobs WHERE book_id=$1`, bookID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM retrieval.metadata_facts WHERE book_id=$1`, bookID)
	})

	work := application.BatchWork{
		EventID:          "batch-event-" + suffix,
		JobID:            newJobID,
		BatchID:          batchID,
		BookID:           bookID,
		SourceSHA256:     sourceSHA256,
		ManifestSHA256:   manifestSHA256,
		ProfileDigest:    profileDigest,
		CorrelationID:    "correlation-" + suffix,
		LifecycleVersion: 2,
	}
	priorJobIDs, err := repository.PriorIndexedJobIDs(ctx, work)
	if err != nil || len(priorJobIDs) != 1 || priorJobIDs[0] != oldJobID {
		t.Fatalf("PriorIndexedJobIDs() = %#v, %v", priorJobIDs, err)
	}
	if err = repository.FinalizeJob(ctx, work, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}

	var activeJobID, state string
	if err = pool.QueryRow(ctx, `SELECT active_job_id,state FROM retrieval.book_lifecycle WHERE book_id=$1`, bookID).Scan(&activeJobID, &state); err != nil {
		t.Fatal(err)
	}
	var oldState, oldFailureCategory string
	var cleanupPending bool
	if err = pool.QueryRow(ctx, `SELECT state,failure_category,vector_cleanup_pending FROM retrieval.index_jobs WHERE id=$1`, oldJobID).Scan(&oldState, &oldFailureCategory, &cleanupPending); err != nil {
		t.Fatal(err)
	}
	var unpublishedOld, publishedOld, indexedEvents int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM retrieval.outbox WHERE aggregate_id=$1 AND published_at IS NULL`, oldJobID).Scan(&unpublishedOld); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM retrieval.outbox WHERE aggregate_id=$1 AND published_at IS NOT NULL`, oldJobID).Scan(&publishedOld); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM retrieval.outbox WHERE event_id=$1`, newJobID+":indexed").Scan(&indexedEvents); err != nil {
		t.Fatal(err)
	}
	if activeJobID != newJobID || state != "active" || oldState != "failed" || oldFailureCategory != string(domain.FailureVectorStoreUnavailable) || !cleanupPending || unpublishedOld != 0 || publishedOld != 1 || indexedEvents != 1 {
		t.Fatalf("active=%q state=%q old_state=%q old_failure_category=%q cleanup_pending=%v unpublished_old=%d published_old=%d indexed_events=%d",
			activeJobID, state, oldState, oldFailureCategory, cleanupPending, unpublishedOld, publishedOld, indexedEvents)
	}
}

func TestRolePrivilegesPlannerAndCleanupCanCompleteDeletion(t *testing.T) {
	if os.Getenv("RETRIEVAL_POSTGRES_INTEGRATION") != "true" {
		t.Skip("set RETRIEVAL_POSTGRES_INTEGRATION=true against an isolated migrated database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	runtimePool, err := pgxpool.New(ctx, readIntegrationDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtimePool.Close)
	roles := []struct {
		name   string
		dsnEnv string
	}{
		{name: "planner", dsnEnv: "RETRIEVAL_PLANNER_POSTGRES_DSN_FILE"},
		{name: "cleanup", dsnEnv: "RETRIEVAL_CLEANUP_POSTGRES_DSN_FILE"},
	}
	for _, role := range roles {
		t.Run(role.name, func(t *testing.T) {
			dsnFile := os.Getenv(role.dsnEnv)
			if dsnFile == "" {
				t.Skipf("set %s to exercise %s-role grants", role.dsnEnv, role.name)
			}
			rolePool, poolErr := pgxpool.New(ctx, readDSNFile(t, dsnFile))
			if poolErr != nil {
				t.Fatal(poolErr)
			}
			t.Cleanup(rolePool.Close)
			exerciseCompleteDeletionRole(t, ctx, runtimePool, rolePool, role.name)
		})
	}
}

func exerciseCompleteDeletionRole(t *testing.T, ctx context.Context, runtimePool, rolePool *pgxpool.Pool, roleName string) {
	t.Helper()
	var err error
	suffix := randomIntegrationID(t)
	bookID := "book-delete-" + suffix
	jobID := "job-delete-" + suffix
	now := time.Now().UTC().Truncate(time.Microsecond)
	sourceSHA256 := integrationDigest(41)
	manifestSHA256 := integrationDigest(42)
	profileDigest := domain.SupportedIndexProfile().Digest
	payloadDigest := integrationDigest(43)
	_, err = runtimePool.Exec(ctx, `INSERT INTO retrieval.metadata_facts
		(book_id,event_id,payload_digest,source_sha256,title,author,publication_year,tags,correlation_id,causation_id,occurred_at)
		VALUES($1,$2,$3,$4,'Sensitive title','Sensitive author',2026,'{private}',$5,$6,$7)`,
		bookID, "metadata-"+suffix, payloadDigest[:], sourceSHA256[:], "correlation-"+suffix, "cause-"+suffix, now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtimePool.Exec(ctx, `INSERT INTO retrieval.manifest_facts
		(book_id,event_id,payload_digest,source_sha256,manifest_sha256,manifest_reference,manifest_payload,correlation_id,causation_id,occurred_at)
		VALUES($1,$2,$3,$4,$5,$6,'manifest',$7,$8,$9)`,
		bookID, "manifest-"+suffix, payloadDigest[:], sourceSHA256[:], manifestSHA256[:], "books/"+bookID+"/manifest.pb",
		"correlation-"+suffix, "cause-"+suffix, now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtimePool.Exec(ctx, `INSERT INTO retrieval.index_jobs
		(id,book_id,source_sha256,manifest_sha256,profile_digest,state,expected_batches,evidence_count,correlation_id,created_at,updated_at,lifecycle_version)
		VALUES($1,$2,$3,$4,$5,'indexed',0,1,$6,$7,$7,1)`,
		jobID, bookID, sourceSHA256[:], manifestSHA256[:], profileDigest[:], "correlation-"+suffix, now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtimePool.Exec(ctx, `INSERT INTO retrieval.book_lifecycle
		(book_id,lifecycle_version,state,event_id,command_id,event_type,payload_digest,cleanup_pending,correlation_id,updated_at)
		VALUES($1,2,'deleting',$2,$3,'deletion',$4,true,$5,$6)`,
		bookID, "delete-"+suffix, "command-"+suffix, payloadDigest[:], "correlation-"+suffix, now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = runtimePool.Exec(cleanupCtx, `DELETE FROM retrieval.outbox WHERE aggregate_id=$1`, bookID)
		_, _ = runtimePool.Exec(cleanupCtx, `DELETE FROM retrieval.book_lifecycle WHERE book_id=$1`, bookID)
		_, _ = runtimePool.Exec(cleanupCtx, `DELETE FROM retrieval.index_jobs WHERE book_id=$1`, bookID)
		_, _ = runtimePool.Exec(cleanupCtx, `DELETE FROM retrieval.manifest_facts WHERE book_id=$1`, bookID)
		_, _ = runtimePool.Exec(cleanupCtx, `DELETE FROM retrieval.metadata_facts WHERE book_id=$1`, bookID)
	})

	cleanup := application.DeletionCleanup{
		BookID:           bookID,
		EventID:          "delete-" + suffix,
		CommandID:        "command-" + suffix,
		CorrelationID:    "correlation-" + suffix,
		LifecycleVersion: 2,
	}
	if err = NewPostgres(rolePool).CompleteDeletion(ctx, cleanup, now.Add(time.Second)); err != nil {
		t.Fatalf("%s-role CompleteDeletion() error = %v", roleName, err)
	}
	if err = NewPostgres(rolePool).CompleteDeletion(ctx, cleanup, now.Add(2*time.Second)); err != nil {
		t.Fatalf("%s-role replayed CompleteDeletion() error = %v", roleName, err)
	}

	var lifecycleState, title, author string
	var tags []string
	var publicationYear int
	var jobCount, manifestCount, deletionEvents int
	if err = runtimePool.QueryRow(ctx, `SELECT state FROM retrieval.book_lifecycle WHERE book_id=$1`, bookID).Scan(&lifecycleState); err != nil {
		t.Fatal(err)
	}
	if err = runtimePool.QueryRow(ctx, `SELECT title,author,publication_year,tags FROM retrieval.metadata_facts WHERE book_id=$1`, bookID).
		Scan(&title, &author, &publicationYear, &tags); err != nil {
		t.Fatal(err)
	}
	if err = runtimePool.QueryRow(ctx, `SELECT count(*) FROM retrieval.index_jobs WHERE book_id=$1`, bookID).Scan(&jobCount); err != nil {
		t.Fatal(err)
	}
	if err = runtimePool.QueryRow(ctx, `SELECT count(*) FROM retrieval.manifest_facts WHERE book_id=$1`, bookID).Scan(&manifestCount); err != nil {
		t.Fatal(err)
	}
	if err = runtimePool.QueryRow(ctx, `SELECT count(*) FROM retrieval.outbox WHERE event_id=$1`, cleanup.EventID+":index-deleted").Scan(&deletionEvents); err != nil {
		t.Fatal(err)
	}
	if lifecycleState != "deleted" || title != "" || author != "" || publicationYear != 0 || len(tags) != 0 ||
		jobCount != 0 || manifestCount != 0 || deletionEvents != 1 {
		t.Fatalf("state=%q title=%q author=%q year=%d tags=%#v jobs=%d manifests=%d events=%d",
			lifecycleState, title, author, publicationYear, tags, jobCount, manifestCount, deletionEvents)
	}
}

func readIntegrationDSN(t *testing.T) string {
	t.Helper()
	return readDSNFile(t, os.Getenv("RETRIEVAL_POSTGRES_DSN_FILE"))
}

func readDSNFile(t *testing.T, path string) string {
	t.Helper()
	file, err := os.Open(path) // #nosec G304 -- operator-owned integration secret path.
	if err != nil {
		t.Fatal("retrieval integration DSN is unavailable")
	}
	defer file.Close()
	contents, err := io.ReadAll(io.LimitReader(file, 4097))
	value := strings.TrimSpace(string(contents))
	if err != nil || len(contents) > 4096 || value == "" {
		t.Fatal("retrieval integration DSN is invalid")
	}
	return value
}

func randomIntegrationID(t *testing.T) string {
	t.Helper()
	value := make([]byte, 8)
	if _, err := rand.Read(value); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(value)
}

func integrationDigest(value byte) [32]byte {
	var digest [32]byte
	digest[0] = value
	return digest
}

func validIntegrationBatchWork(bookID, jobID, batchID string, now time.Time) application.BatchWork {
	profile := domain.SupportedIndexProfile()
	return application.BatchWork{
		EventID:              "event-" + batchID,
		JobID:                jobID,
		BatchID:              batchID,
		BookID:               bookID,
		ShardReference:       "books/" + bookID + "/source/profile/shards/000000.pb.zst",
		ShardSHA256:          integrationDigest(1),
		SourceSHA256:         integrationDigest(2),
		ManifestSHA256:       integrationDigest(3),
		ProfileDigest:        profile.Digest,
		CompressedBytes:      10,
		UncompressedBytes:    20,
		ChunkCount:           1,
		ManifestPageCount:    1,
		FirstChunkOrder:      0,
		LastChunkOrder:       0,
		ExtractionVersion:    profile.ExtractionVersion,
		NormalizationVersion: profile.NormalizationVersion,
		TokenizerVersion:     profile.TokenizerVersion,
		ChunkingVersion:      profile.ChunkingVersion,
		StructureVersion:     profile.StructureVersion,
		MaximumTokens:        uint32(profile.MaximumTokens),
		OverlapTokens:        uint32(profile.OverlapTokens),
		CorrelationID:        "correlation-" + batchID,
		CausationID:          "cause-" + batchID,
		Producer:             "retrieval-service",
		SchemaVersion:        "v1",
		IdempotencyKey:       batchID,
		OccurredAt:           now,
	}
}

func manifestForWork(work application.BatchWork) application.Manifest {
	return application.Manifest{
		SchemaVersion:          "v1",
		BookID:                 work.BookID,
		SourceSHA256:           work.SourceSHA256,
		ManifestSHA256:         work.ManifestSHA256,
		ProcessingConfigDigest: integrationDigest(7),
		PageCount:              work.ManifestPageCount,
		ChunkCount:             work.ChunkCount,
		GeneratedAt:            work.OccurredAt.Add(-time.Minute),
		ExtractionVersion:      work.ExtractionVersion,
		NormalizationVersion:   work.NormalizationVersion,
		TokenizerVersion:       work.TokenizerVersion,
		ChunkingVersion:        work.ChunkingVersion,
		StructureVersion:       work.StructureVersion,
		MaximumTokens:          work.MaximumTokens,
		OverlapTokens:          work.OverlapTokens,
		Shards: []application.Shard{{
			Reference:         work.ShardReference,
			SHA256:            work.ShardSHA256,
			CompressedBytes:   work.CompressedBytes,
			UncompressedBytes: work.UncompressedBytes,
			ChunkCount:        work.ChunkCount,
			FirstChunkOrder:   work.FirstChunkOrder,
			LastChunkOrder:    work.LastChunkOrder,
		}},
	}
}

func insertManifestFact(t *testing.T, ctx context.Context, pool *pgxpool.Pool, bookID string, sourceSHA256, manifestSHA256 [32]byte, correlationID, causationID string, occurredAt time.Time, manifest application.Manifest) {
	t.Helper()
	payload, err := encodeManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	payloadDigest := integrationDigest(9)
	_, err = pool.Exec(ctx, `INSERT INTO retrieval.manifest_facts(book_id,event_id,payload_digest,source_sha256,manifest_sha256,manifest_reference,manifest_payload,correlation_id,causation_id,occurred_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		bookID,
		"manifest-"+bookID,
		payloadDigest[:],
		sourceSHA256[:],
		manifestSHA256[:],
		"books/"+bookID+"/source/profile/manifest.pb",
		payload,
		correlationID,
		causationID,
		occurredAt,
	)
	if err != nil {
		t.Fatal(err)
	}
}
