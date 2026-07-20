//go:build integration

package repository

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/application"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/domain"
	"github.com/jackc/pgx/v5/pgxpool"
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
	if recovered, recoverErr := repository.RecoverStaleBatches(ctx, now.Add(-30*time.Minute), now); recoverErr != nil || recovered != 1 {
		t.Fatalf("RecoverStaleBatches() recovered=%d error=%v", recovered, recoverErr)
	}
	if _, accepted, beginErr := repository.BeginBatch(ctx, work); beginErr != nil || !accepted {
		t.Fatalf("replayed BeginBatch() accepted=%v error=%v", accepted, beginErr)
	}
	if err = repository.FailBatch(ctx, work, domain.FailureInternalIndexing, now); err != nil {
		t.Fatal(err)
	}
	if err = repository.FailBatch(ctx, work, domain.FailureInternalIndexing, now.Add(time.Second)); err != nil {
		t.Fatal(err)
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
