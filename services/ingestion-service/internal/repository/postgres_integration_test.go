//go:build integration

package repository

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/application"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/artifact"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/chunking"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/domain"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestE2ERoleCanReadOnlyRequiredM4IngestionTables(t *testing.T) {
	if os.Getenv("INGESTION_POSTGRES_INTEGRATION") != "true" {
		t.Skip("set INGESTION_POSTGRES_INTEGRATION=true inside the Compose test network")
	}
	if os.Getenv("M4_E2E_INGESTION_POSTGRES_DSN_FILE") == "" {
		t.Skip("set M4_E2E_INGESTION_POSTGRES_DSN_FILE inside the Compose test network")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, readIngestionIntegrationSecret(t, "M4_E2E_INGESTION_POSTGRES_DSN_FILE"))
	if err != nil {
		t.Fatalf("connect ingestion e2e database role: %v", err)
	}
	t.Cleanup(pool.Close)

	for _, statement := range []string{
		`SELECT COUNT(*) FROM ingestion.inbox`,
		`SELECT COUNT(*) FROM ingestion.jobs`,
		`SELECT COUNT(*) FROM ingestion.artifact_sets`,
	} {
		var count int
		if err = pool.QueryRow(ctx, statement).Scan(&count); err != nil {
			t.Fatalf("e2e role cannot read with %q: %v", statement, err)
		}
	}

	_, err = pool.Exec(ctx, `INSERT INTO ingestion.inbox
		(event_id,payload_digest,payload,business_key,source_sha256,processing_config_digest,received_at)
		VALUES('e2e-write-denied',decode(repeat('00',32),'hex'),decode('01','hex'),'e2e-write-denied',decode(repeat('00',32),'hex'),decode(repeat('00',32),'hex'),NOW())`)
	if !isInsufficientPrivilege(err) {
		t.Fatalf("e2e role write error = %v, want insufficient_privilege", err)
	}
}

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
	if err = NewPostgres(pool, Policy{RetryDispatchDelay: time.Second, OutboxRetryBaseDelay: time.Second, OutboxRetryMaxDelay: 5 * time.Minute}).Retry(ctx, job, claim); err != nil {
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

func TestDeletionBarrierWaitsForActiveLeaseAndCleanupRoleCanFinalize(t *testing.T) {
	if os.Getenv("INGESTION_POSTGRES_INTEGRATION") != "true" {
		t.Skip("set INGESTION_POSTGRES_INTEGRATION=true inside the Compose test network")
	}
	if os.Getenv("INGESTION_CLEANUP_POSTGRES_DSN_FILE") == "" {
		t.Skip("set INGESTION_CLEANUP_POSTGRES_DSN_FILE inside the Compose test network")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	runtimePool, err := pgxpool.New(ctx, readIngestionIntegrationSecret(t, "INGESTION_POSTGRES_DSN_FILE"))
	if err != nil {
		t.Fatalf("connect ingestion runtime role: %v", err)
	}
	t.Cleanup(runtimePool.Close)
	cleanupPool, err := pgxpool.New(ctx, readIngestionIntegrationSecret(t, "INGESTION_CLEANUP_POSTGRES_DSN_FILE"))
	if err != nil {
		t.Fatalf("connect ingestion cleanup role: %v", err)
	}
	t.Cleanup(cleanupPool.Close)

	suffix := randomRepositoryIntegrationID(t)
	jobID := "delete-job-" + suffix
	bookID := "delete-book-" + suffix
	eventID := "delete-event-" + suffix
	commandID := "delete-command-" + suffix
	ackID := "delete-ack-" + suffix
	now := time.Now().UTC().Truncate(time.Microsecond)
	leaseExpiresAt := now.Add(2 * time.Minute)
	sourceSHA256 := [32]byte{1}
	configDigest := [32]byte{2}
	prefix := "books/" + bookID + "/" + hex.EncodeToString(sourceSHA256[:]) + "/" + hex.EncodeToString(configDigest[:]) + "/"

	_, err = runtimePool.Exec(ctx, `INSERT INTO ingestion.jobs
		(id,book_id,source_sha256,processing_config_digest,state,attempts,lease_owner,lease_expires_at,
		 structure_version,maximum_tokens,overlap_tokens,created_at,updated_at,lifecycle_version,
		 manifest_reference,manifest_sha256,manifest_byte_size)
		VALUES($1,$2,$3,$4,'processing',1,'worker-1',$5,$6,$7,$8,$9,$9,1,$10,$11,8)`,
		jobID, bookID, sourceSHA256[:], configDigest[:], leaseExpiresAt,
		chunking.StructureVersion, chunking.DefaultMaximumTokens, chunking.DefaultOverlapTokens,
		now, prefix+"manifest.pb", sourceSHA256[:])
	if err != nil {
		t.Fatalf("insert active job fixture: %v", err)
	}
	_, err = runtimePool.Exec(ctx, `INSERT INTO ingestion.artifact_sets
		(job_id,prefix,manifest_reference,manifest_sha256,structure_version,maximum_tokens,overlap_tokens,
		 committed_at,updated_at,lifecycle_version)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$8,1)`,
		jobID, prefix, prefix+"manifest.pb", sourceSHA256[:], chunking.StructureVersion,
		chunking.DefaultMaximumTokens, chunking.DefaultOverlapTokens, now)
	if err != nil {
		t.Fatalf("insert artifact fixture: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = runtimePool.Exec(cleanupCtx, "DELETE FROM ingestion.outbox WHERE event_id=$1", ackID)
		_, _ = runtimePool.Exec(cleanupCtx, "DELETE FROM ingestion.artifact_sets WHERE job_id=$1", jobID)
		_, _ = runtimePool.Exec(cleanupCtx, "DELETE FROM ingestion.jobs WHERE id=$1", jobID)
		_, _ = runtimePool.Exec(cleanupCtx, "DELETE FROM ingestion.deletion_inbox WHERE event_id=$1", eventID)
		_, _ = runtimePool.Exec(cleanupCtx, "DELETE FROM ingestion.lifecycle_fences WHERE book_id=$1", bookID)
	})

	deletion := application.DeletionEvent{
		EventID: eventID, BookID: bookID, CommandID: commandID, LifecycleVersion: 2,
		OccurredAt: now,
	}
	ack := application.OutboxEvent{
		ID: ackID, Type: "ingestion.book.artifacts-deleted.v1", Payload: []byte{1}, OccurredAt: now,
	}
	if err = NewPostgres(runtimePool, Policy{RetryDispatchDelay: time.Second, OutboxRetryBaseDelay: time.Second, OutboxRetryMaxDelay: 5 * time.Minute}).AcceptDeletion(ctx, deletion, sourceSHA256, ack, now); err != nil {
		t.Fatalf("accept deletion: %v", err)
	}

	var cleanupAfter time.Time
	if err = runtimePool.QueryRow(ctx, `SELECT cleanup_after FROM ingestion.artifact_sets WHERE job_id=$1`, jobID).Scan(&cleanupAfter); err != nil {
		t.Fatalf("read cleanup barrier: %v", err)
	}
	if !cleanupAfter.Equal(leaseExpiresAt) {
		t.Fatalf("cleanup_after = %v, want active lease %v", cleanupAfter, leaseExpiresAt)
	}
	claimed, err := NewPostgres(runtimePool, Policy{RetryDispatchDelay: time.Second, OutboxRetryBaseDelay: time.Second, OutboxRetryMaxDelay: 5 * time.Minute}).ClaimDeletionArtifacts(ctx, now, time.Minute, 10)
	if err != nil {
		t.Fatalf("claim before lease: %v", err)
	}
	if len(claimed) != 0 {
		t.Fatalf("claimed before active writer lease elapsed: %#v", claimed)
	}
	finalizedAt := now.Add(time.Second)
	job, err := domain.RestoreProcessingJob(
		jobID,
		bookID,
		sourceSHA256,
		hex.EncodeToString(configDigest[:]),
		domain.JobProcessing,
		1,
		"worker-1",
		leaseExpiresAt,
		time.Time{},
		"",
		"",
		[32]byte{},
		0,
		now,
		finalizedAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	claim := application.ClaimToken{Owner: "worker-1", Attempt: 1, ExpiresAt: leaseExpiresAt}
	if err = NewPostgres(runtimePool, Policy{RetryDispatchDelay: time.Second, OutboxRetryBaseDelay: time.Second, OutboxRetryMaxDelay: 5 * time.Minute}).Complete(ctx, job, claim, artifact.Result{}, application.OutboxEvent{}); err != nil {
		t.Fatalf("fenced completion reschedule: %v", err)
	}
	if err = runtimePool.QueryRow(ctx, `SELECT cleanup_after FROM ingestion.artifact_sets WHERE job_id=$1`, jobID).Scan(&cleanupAfter); err != nil {
		t.Fatalf("read post-finalize cleanup barrier: %v", err)
	}
	if !cleanupAfter.Equal(finalizedAt) {
		t.Fatalf("cleanup_after = %v, want finalized time %v", cleanupAfter, finalizedAt)
	}
	claimed, err = NewPostgres(runtimePool, Policy{RetryDispatchDelay: time.Second, OutboxRetryBaseDelay: time.Second, OutboxRetryMaxDelay: 5 * time.Minute}).ClaimDeletionArtifacts(ctx, finalizedAt, time.Minute, 10)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim after fenced final write = (%#v, %v), want one artifact", claimed, err)
	}

	if err = NewPostgres(cleanupPool, Policy{RetryDispatchDelay: time.Second, OutboxRetryBaseDelay: time.Second, OutboxRetryMaxDelay: 5 * time.Minute}).CompleteDeletionArtifact(ctx, eventID, jobID, finalizedAt); err != nil {
		t.Fatalf("cleanup role finalize deletion: %v", err)
	}
	var outboxCount int
	if err = runtimePool.QueryRow(ctx, `SELECT count(*) FROM ingestion.outbox WHERE event_id=$1`, ackID).Scan(&outboxCount); err != nil {
		t.Fatalf("read deletion acknowledgment: %v", err)
	}
	if outboxCount != 1 {
		t.Fatalf("acknowledgment count = %d, want 1", outboxCount)
	}
}

func TestAcceptRollsBackEarlierWritesWhenOutboxInsertFails(t *testing.T) {
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
	bookID := "atomic-book-" + suffix
	jobID := "atomic-job-" + suffix
	eventID := "atomic-event-" + suffix
	startedID := "atomic-started-" + suffix
	now := time.Now().UTC().Truncate(time.Microsecond)
	sourceSHA256 := [32]byte{1}
	configDigest := [32]byte{2}
	proposed, err := domain.NewProcessingJob(jobID, bookID, sourceSHA256, hex.EncodeToString(configDigest[:]), now)
	if err != nil {
		t.Fatal(err)
	}
	event := application.UploadedEvent{
		EventID:           eventID,
		BookID:            bookID,
		ObjectReference:   "originals/" + bookID + ".pdf",
		MediaType:         "application/pdf",
		CorrelationID:     "correlation-" + suffix,
		CausationID:       "cause-" + suffix,
		Producer:          "catalog-service",
		SchemaVersion:     "v1",
		IdempotencyKey:    bookID,
		SourceSHA256:      sourceSHA256,
		ByteSize:          1,
		LifecycleVersion:  1,
		OccurredAt:        now,
		Payload:           []byte("upload"),
		ExtractionVersion: "poppler-layout-v1",
	}
	_, err = pool.Exec(ctx, `INSERT INTO ingestion.outbox
		(event_id,event_type,aggregate_id,aggregate_sequence,payload,occurred_at,next_attempt_at)
		VALUES($1,'ingestion.book.processing-started.v1',$2,1,'seeded duplicate',$3,$3)`,
		startedID, jobID, now)
	if err != nil {
		t.Fatalf("seed duplicate started event: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM ingestion.outbox WHERE event_id=$1", startedID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM ingestion.artifact_sets WHERE job_id=$1", jobID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM ingestion.jobs WHERE id=$1", jobID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM ingestion.inbox WHERE event_id=$1", eventID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM ingestion.lifecycle_fences WHERE book_id=$1", bookID)
	})

	accepted, _, acceptErr := NewPostgres(pool, Policy{RetryDispatchDelay: time.Second, OutboxRetryBaseDelay: time.Second, OutboxRetryMaxDelay: 5 * time.Minute}).Accept(ctx, event, sourceSHA256, proposed, application.OutboxEvent{
		ID: startedID, Type: "ingestion.book.processing-started.v1", Payload: []byte("started"), OccurredAt: now,
	})
	if acceptErr == nil || accepted.ID() != proposed.ID() {
		t.Fatalf("Accept() accepted=%+v error=%v", accepted, acceptErr)
	}
	var inboxCount, jobCount, artifactCount, fenceCount int
	if err = pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM ingestion.inbox WHERE event_id=$1),
		(SELECT count(*) FROM ingestion.jobs WHERE id=$2),
		(SELECT count(*) FROM ingestion.artifact_sets WHERE job_id=$2),
		(SELECT count(*) FROM ingestion.lifecycle_fences WHERE book_id=$3)`,
		eventID, jobID, bookID).Scan(&inboxCount, &jobCount, &artifactCount, &fenceCount); err != nil {
		t.Fatalf("read rolled back ingestion projection: %v", err)
	}
	if inboxCount != 0 || jobCount != 0 || artifactCount != 0 || fenceCount != 0 {
		t.Fatalf("rolled back counts inbox=%d jobs=%d artifacts=%d fences=%d", inboxCount, jobCount, artifactCount, fenceCount)
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

func isInsufficientPrivilege(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "42501"
}
