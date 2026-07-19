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

	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/application"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/chunking"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/domain"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRetryAdvancesPendingActiveLeaseRecoveryDispatch(t *testing.T) {
	if os.Getenv("INGESTION_POSTGRES_INTEGRATION") != "true" {
		t.Skip("set INGESTION_POSTGRES_INTEGRATION=true inside the Compose test network")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, readIngestionIntegrationSecret(t, "INGESTION_POSTGRES_DSN_FILE"))
	if err != nil {
		t.Fatalf("connect ingestion database: %v", err)
	}
	t.Cleanup(pool.Close)

	suffix := randomRepositoryIntegrationID(t)
	jobID := "retry-job-" + suffix
	bookID := "retry-book-" + suffix
	eventID := "retry-event-" + suffix
	payload := []byte("bounded-retry-payload")
	sourceSHA256 := [32]byte{1}
	configDigest := [32]byte{2}
	now := time.Now().UTC().Truncate(time.Microsecond)
	leaseExpiresAt := now.Add(13 * time.Minute)
	retryAt := now.Add(5 * time.Second)

	_, err = pool.Exec(ctx, `INSERT INTO ingestion.inbox
		(event_id,payload_digest,payload,business_key,source_sha256,processing_config_digest,received_at)
		VALUES($1,$2,$3,$4,$5,$6,$7)`, eventID, sourceSHA256[:], payload, bookID, sourceSHA256[:], configDigest[:], now)
	if err != nil {
		t.Fatalf("insert inbox fixture: %v", err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO ingestion.jobs
		(id,book_id,source_sha256,processing_config_digest,state,attempts,lease_owner,lease_expires_at,structure_version,maximum_tokens,overlap_tokens,created_at,updated_at)
		VALUES($1,$2,$3,$4,'processing',1,'worker-1',$5,$6,$7,$8,$9,$9)`,
		jobID, bookID, sourceSHA256[:], configDigest[:], leaseExpiresAt, chunking.StructureVersion,
		chunking.DefaultMaximumTokens, chunking.DefaultOverlapTokens, now)
	if err != nil {
		t.Fatalf("insert job fixture: %v", err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO ingestion.retry_dispatches
		(job_id,attempt,event_id,payload,dispatch_after,next_attempt_at)
		VALUES($1,1,$2,$3,$4,$4)`, jobID, eventID, payload, leaseExpiresAt)
	if err != nil {
		t.Fatalf("insert recovery fixture: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM ingestion.jobs WHERE id=$1", jobID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM ingestion.inbox WHERE event_id=$1", eventID)
	})

	job, err := domain.RestoreProcessingJob(jobID, bookID, sourceSHA256, hex.EncodeToString(configDigest[:]), domain.JobProcessing,
		1, "worker-1", leaseExpiresAt, time.Time{}, "", "", [32]byte{}, 0, now, now)
	if err != nil {
		t.Fatal(err)
	}
	claim := application.ClaimToken{Owner: "worker-1", Attempt: 1, ExpiresAt: leaseExpiresAt}
	if err = job.ScheduleRetry(claim.Owner, retryAt, now); err != nil {
		t.Fatal(err)
	}
	if err = NewPostgres(pool).Retry(ctx, job, claim); err != nil {
		t.Fatalf("schedule real retry: %v", err)
	}

	var dispatchAfter, nextAttemptAt time.Time
	var publishedAt *time.Time
	if err = pool.QueryRow(ctx, `SELECT dispatch_after,next_attempt_at,published_at
		FROM ingestion.retry_dispatches WHERE job_id=$1 AND attempt=1`, jobID).Scan(&dispatchAfter, &nextAttemptAt, &publishedAt); err != nil {
		t.Fatalf("read advanced dispatch: %v", err)
	}
	if !dispatchAfter.Equal(retryAt) || !nextAttemptAt.Equal(now) || publishedAt != nil {
		t.Fatalf("dispatch schedule = (%v,%v,%v), want (%v,%v,nil)", dispatchAfter, nextAttemptAt, publishedAt, retryAt, now)
	}
}

func readIngestionIntegrationSecret(t *testing.T, key string) string {
	t.Helper()
	path := os.Getenv(key)
	file, err := os.Open(path) // #nosec G304 -- integration-only operator-provided secret path.
	if err != nil {
		t.Fatalf("%s is unavailable", key)
	}
	defer func() { _ = file.Close() }()
	contents, err := io.ReadAll(io.LimitReader(file, 4097))
	value := strings.TrimSpace(string(contents))
	if err != nil || len(contents) > 4096 || value == "" {
		t.Fatalf("%s is invalid", key)
	}
	return value
}

func randomRepositoryIntegrationID(t *testing.T) string {
	t.Helper()
	value := make([]byte, 8)
	if _, err := rand.Read(value); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(value)
}
