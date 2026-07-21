//go:build integration

package repository

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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

func readIntegrationDSN(t *testing.T) string {
	t.Helper()
	file, err := os.Open(os.Getenv("RETRIEVAL_POSTGRES_DSN_FILE")) // #nosec G304 -- operator-owned integration secret path.
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
