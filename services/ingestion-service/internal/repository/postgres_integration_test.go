//go:build integration

package repository

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
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
		`SELECT COUNT(*) FROM ingestion.content_selection_inbox`,
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

func TestContentSelectionAwaitResumeIsAtomicAndIdempotent(t *testing.T) {
	if os.Getenv("INGESTION_POSTGRES_INTEGRATION") != "true" {
		t.Skip("set INGESTION_POSTGRES_INTEGRATION=true inside the isolated Compose test network")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, readIngestionIntegrationSecret(t, "INGESTION_POSTGRES_DSN_FILE"))
	if err != nil {
		t.Fatalf("connect ingestion database: %v", err)
	}
	t.Cleanup(pool.Close)

	suffix := randomRepositoryIntegrationID(t)
	bookID := "selection-book-" + suffix
	jobID := "selection-job-" + suffix
	uploadID := "selection-upload-" + suffix
	requestID := "selection-request-" + suffix
	resultID := "selection-result-" + suffix
	now := time.Now().UTC().Truncate(time.Microsecond)
	sourceSHA256 := [32]byte{1}
	configDigest := [32]byte{2}
	uploadPayload := []byte("bounded-upload-envelope")
	job, err := domain.NewProcessingJob(jobID, bookID, sourceSHA256, hex.EncodeToString(configDigest[:]), now)
	if err != nil {
		t.Fatal(err)
	}
	if err = job.Claim("worker-1", now, 15*time.Minute); err != nil {
		t.Fatal(err)
	}
	event := application.UploadedEvent{
		EventID: uploadID, BookID: bookID, ObjectReference: "originals/" + bookID + ".pdf", MediaType: application.MediaTypePDF,
		CorrelationID: "correlation-" + suffix, CausationID: "cause-" + suffix, Producer: "catalog-service", SchemaVersion: "v1", IdempotencyKey: bookID,
		SourceSHA256: sourceSHA256, ByteSize: 1, LifecycleVersion: 1, OccurredAt: now, Payload: uploadPayload,
	}
	repository := NewPostgres(pool, testPolicy())
	acceptedResult, err := repository.Accept(ctx, event, [32]byte{3}, job, application.OutboxEvent{
		ID: "selection-started-" + suffix, Type: "ingestion.book.processing-started.v1", Payload: []byte("started"), OccurredAt: now,
	})
	if err != nil || !acceptedResult.Accepted {
		t.Fatalf("Accept() accepted=%t error=%v", acceptedResult.Accepted, err)
	}
	acceptedJob := acceptedResult.Job
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM ingestion.jobs WHERE id=$1", jobID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM ingestion.inbox WHERE event_id=$1", uploadID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM ingestion.lifecycle_fences WHERE book_id=$1", bookID)
	})
	claim := application.ClaimToken{Owner: acceptedJob.LeaseOwner(), Attempt: acceptedJob.Attempts(), ExpiresAt: acceptedJob.LeaseExpiresAt()}
	if err = acceptedJob.AwaitContentSelection(claim.Owner, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err = repository.AwaitSelection(ctx, acceptedJob, claim, application.OutboxEvent{
		ID: requestID, Type: "ingestion.book.content-selection-requested.v1", Payload: []byte("request"), OccurredAt: now.Add(time.Second),
	}); err != nil {
		t.Fatalf("AwaitSelection() error = %v", err)
	}
	var waitDispatchAfter, waitNextAttemptAt time.Time
	if err = pool.QueryRow(ctx, `SELECT dispatch_after,next_attempt_at FROM ingestion.retry_dispatches
		WHERE job_id=$1 AND attempt=$2`, jobID, acceptedJob.Attempts()).Scan(&waitDispatchAfter, &waitNextAttemptAt); err != nil {
		t.Fatalf("load content-selection wait recovery dispatch: %v", err)
	}
	wantSelectionRetryAt := acceptedJob.UpdatedAt().Add(testPolicy().ContentSelectionWaitTimeout)
	if !waitDispatchAfter.Equal(wantSelectionRetryAt) || !waitNextAttemptAt.Equal(wantSelectionRetryAt) {
		t.Fatalf("content-selection wait recovery = dispatch_after %v next_attempt_at %v, want %v", waitDispatchAfter, waitNextAttemptAt, wantSelectionRetryAt)
	}
	resultPayload := []byte("bounded-selection-result")
	resultDigest := sha256.Sum256(resultPayload)
	record := application.ContentSelectionRecord{
		EventID: resultID, RequestID: requestID, JobID: jobID, BookID: bookID, PayloadDigest: resultDigest, Payload: resultPayload,
		SourceSHA256: sourceSHA256, ProcessingProfileDigest: configDigest, LifecycleVersion: 1, ReceivedAt: now.Add(2 * time.Second),
	}
	resumed, resumedNow, err := repository.AcceptContentSelection(ctx, record, "worker-2", now.Add(2*time.Second), 15*time.Minute)
	if err != nil || !resumedNow || resumed.State() != domain.JobProcessing {
		t.Fatalf("AcceptContentSelection() resumed=%t state=%q error=%v", resumedNow, resumed.State(), err)
	}
	if stored, loadErr := repository.LoadUploadedPayload(ctx, jobID); loadErr != nil || string(stored) != string(uploadPayload) {
		t.Fatalf("LoadUploadedPayload() payload=%q error=%v", stored, loadErr)
	}
	_, duplicateAccepted, err := repository.AcceptContentSelection(ctx, record, "worker-3", now.Add(3*time.Second), 15*time.Minute)
	if !errors.Is(err, application.ErrProcessingDeferred) || duplicateAccepted {
		t.Fatalf("active duplicate result accepted=%t error=%v", duplicateAccepted, err)
	}
	var dispatchAfter, nextAttemptAt time.Time
	if err = pool.QueryRow(ctx, `SELECT dispatch_after,next_attempt_at FROM ingestion.retry_dispatches
		WHERE job_id=$1 AND attempt=$2`, jobID, resumed.Attempts()).Scan(&dispatchAfter, &nextAttemptAt); err != nil {
		t.Fatalf("load recovery dispatch: %v", err)
	}
	if !dispatchAfter.Equal(resumed.LeaseExpiresAt()) || !nextAttemptAt.Equal(resumed.LeaseExpiresAt()) {
		t.Fatalf("recovery dispatch = dispatch_after %v next_attempt_at %v, want %v", dispatchAfter, nextAttemptAt, resumed.LeaseExpiresAt())
	}
	reclaimed, reclaimedNow, err := repository.AcceptContentSelection(ctx, record, "worker-3", resumed.LeaseExpiresAt().Add(time.Second), 15*time.Minute)
	if err != nil || !reclaimedNow {
		t.Fatalf("expired duplicate result accepted=%t error=%v", reclaimedNow, err)
	}
	if reclaimed.State() != domain.JobProcessing || reclaimed.LeaseOwner() != "worker-3" ||
		!reclaimed.LeaseExpiresAt().After(resumed.LeaseExpiresAt()) || reclaimed.Attempts() != resumed.Attempts()+1 {
		t.Fatalf("reclaimed job = state %q owner %q lease %v attempts %d", reclaimed.State(), reclaimed.LeaseOwner(), reclaimed.LeaseExpiresAt(), reclaimed.Attempts())
	}
	conflict := record
	conflict.EventID = "selection-conflict-" + suffix
	conflict.Payload = []byte("conflicting-result")
	conflict.PayloadDigest = sha256.Sum256(conflict.Payload)
	if _, _, err = repository.AcceptContentSelection(ctx, conflict, "worker-4", reclaimed.LeaseExpiresAt().Add(time.Second), 15*time.Minute); !errors.Is(err, application.ErrConflictingEvent) {
		t.Fatalf("conflicting result error = %v", err)
	}
}

func TestAwaitingSelectionUploadRedeliveryRecoversAfterWaitTimeout(t *testing.T) {
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
	jobID := "selection-timeout-job-" + suffix
	bookID := "selection-timeout-book-" + suffix
	uploadID := "selection-timeout-upload-" + suffix
	requestID := "selection-timeout-request-" + suffix
	sourceSHA256 := [32]byte{1}
	configDigest := [32]byte{2}
	payloadDigest := [32]byte{3}
	uploadPayload := []byte("bounded-upload-envelope")
	now := time.Now().UTC().Truncate(time.Microsecond)
	policy := testPolicy()
	repository := NewPostgres(pool, policy)
	job, err := domain.NewProcessingJob(jobID, bookID, sourceSHA256, hex.EncodeToString(configDigest[:]), now)
	if err != nil {
		t.Fatal(err)
	}
	if err = job.Claim("worker-1", now, 15*time.Minute); err != nil {
		t.Fatal(err)
	}
	event := application.UploadedEvent{
		EventID:          uploadID,
		BookID:           bookID,
		ObjectReference:  "originals/" + bookID + ".pdf",
		MediaType:        application.MediaTypePDF,
		CorrelationID:    "correlation-" + suffix,
		CausationID:      "cause-" + suffix,
		Producer:         "catalog-service",
		SchemaVersion:    "v1",
		IdempotencyKey:   bookID,
		SourceSHA256:     sourceSHA256,
		ByteSize:         1,
		LifecycleVersion: 1,
		OccurredAt:       now,
		Payload:          uploadPayload,
	}
	acceptedResult, err := repository.Accept(ctx, event, payloadDigest, job, application.OutboxEvent{
		ID: "selection-timeout-started-" + suffix, Type: "ingestion.book.processing-started.v1", Payload: []byte("started"), OccurredAt: now,
	})
	if err != nil || !acceptedResult.Accepted {
		t.Fatalf("Accept() accepted=%t error=%v", acceptedResult.Accepted, err)
	}
	acceptedJob := acceptedResult.Job
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM ingestion.jobs WHERE id=$1", jobID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM ingestion.inbox WHERE event_id=$1", uploadID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM ingestion.lifecycle_fences WHERE book_id=$1", bookID)
	})
	claim := application.ClaimToken{Owner: acceptedJob.LeaseOwner(), Attempt: acceptedJob.Attempts(), ExpiresAt: acceptedJob.LeaseExpiresAt()}
	awaitAt := now.Add(time.Second)
	if err = acceptedJob.AwaitContentSelection(claim.Owner, awaitAt); err != nil {
		t.Fatal(err)
	}
	if err = repository.AwaitSelection(ctx, acceptedJob, claim, application.OutboxEvent{
		ID: requestID, Type: "ingestion.book.content-selection-requested.v1", Payload: []byte("request"), OccurredAt: awaitAt,
	}); err != nil {
		t.Fatalf("AwaitSelection() error = %v", err)
	}

	beforeTimeout, err := domain.NewProcessingJob("selection-timeout-before-"+suffix, bookID, sourceSHA256, hex.EncodeToString(configDigest[:]), awaitAt.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err = beforeTimeout.Claim("worker-2", beforeTimeout.CreatedAt(), 15*time.Minute); err != nil {
		t.Fatal(err)
	}
	beforeResult, err := repository.Accept(ctx, event, payloadDigest, beforeTimeout, application.OutboxEvent{
		ID: "selection-timeout-before-started-" + suffix, Type: "ingestion.book.processing-started.v1", Payload: []byte("started"), OccurredAt: beforeTimeout.CreatedAt(),
	})
	if !errors.Is(err, application.ErrProcessingDeferred) || beforeResult.Accepted {
		t.Fatalf("before timeout Accept() accepted=%t error=%v", beforeResult.Accepted, err)
	}

	afterAt := awaitAt.Add(policy.ContentSelectionWaitTimeout).Add(time.Second)
	afterTimeout, err := domain.NewProcessingJob("selection-timeout-after-"+suffix, bookID, sourceSHA256, hex.EncodeToString(configDigest[:]), afterAt)
	if err != nil {
		t.Fatal(err)
	}
	if err = afterTimeout.Claim("worker-3", afterAt, 15*time.Minute); err != nil {
		t.Fatal(err)
	}
	afterResult, err := repository.Accept(ctx, event, payloadDigest, afterTimeout, application.OutboxEvent{
		ID: "selection-timeout-after-started-" + suffix, Type: "ingestion.book.processing-started.v1", Payload: []byte("started"), OccurredAt: afterAt,
	})
	if err != nil || !afterResult.Accepted || !afterResult.ContentSelectionTimedOut {
		t.Fatalf("after timeout Accept() result=%+v error=%v", afterResult, err)
	}
	if afterResult.Job.State() != domain.JobProcessing || afterResult.Job.LeaseOwner() != "worker-3" ||
		afterResult.Job.Attempts() != acceptedJob.Attempts()+1 {
		t.Fatalf("after timeout job = state %q owner %q attempts %d", afterResult.Job.State(), afterResult.Job.LeaseOwner(), afterResult.Job.Attempts())
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
	if err = NewPostgres(pool, testPolicy()).Retry(ctx, job, claim); err != nil {
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
	if err = NewPostgres(runtimePool, testPolicy()).AcceptDeletion(ctx, deletion, sourceSHA256, ack, now); err != nil {
		t.Fatalf("accept deletion: %v", err)
	}

	var cleanupAfter time.Time
	if err = runtimePool.QueryRow(ctx, `SELECT cleanup_after FROM ingestion.artifact_sets WHERE job_id=$1`, jobID).Scan(&cleanupAfter); err != nil {
		t.Fatalf("read cleanup barrier: %v", err)
	}
	if !cleanupAfter.Equal(leaseExpiresAt) {
		t.Fatalf("cleanup_after = %v, want active lease %v", cleanupAfter, leaseExpiresAt)
	}
	claimed, err := NewPostgres(runtimePool, testPolicy()).ClaimDeletionArtifacts(ctx, now, time.Minute, 10)
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
	if err = NewPostgres(runtimePool, testPolicy()).Complete(ctx, job, claim, artifact.Result{}, application.OutboxEvent{}); err != nil {
		t.Fatalf("fenced completion reschedule: %v", err)
	}
	if err = runtimePool.QueryRow(ctx, `SELECT cleanup_after FROM ingestion.artifact_sets WHERE job_id=$1`, jobID).Scan(&cleanupAfter); err != nil {
		t.Fatalf("read post-finalize cleanup barrier: %v", err)
	}
	if !cleanupAfter.Equal(finalizedAt) {
		t.Fatalf("cleanup_after = %v, want finalized time %v", cleanupAfter, finalizedAt)
	}
	claimed, err = NewPostgres(runtimePool, testPolicy()).ClaimDeletionArtifacts(ctx, finalizedAt, time.Minute, 10)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim after fenced final write = (%#v, %v), want one artifact", claimed, err)
	}

	cleanupRepository := NewPostgres(cleanupPool, testPolicy())
	start := make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			results <- cleanupRepository.CompleteDeletionArtifact(ctx, eventID, jobID, finalizedAt)
		}()
	}
	close(start)
	for range 2 {
		if completionErr := <-results; completionErr != nil {
			t.Fatalf("concurrent cleanup role finalize deletion: %v", completionErr)
		}
	}
	if err = cleanupRepository.CompleteDeletionArtifact(ctx, eventID, jobID, finalizedAt); err != nil {
		t.Fatalf("repeated cleanup role finalize deletion: %v", err)
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

	accepted, acceptErr := NewPostgres(pool, testPolicy()).Accept(ctx, event, sourceSHA256, proposed, application.OutboxEvent{
		ID: startedID, Type: "ingestion.book.processing-started.v1", Payload: []byte("started"), OccurredAt: now,
	})
	if acceptErr == nil || accepted.Job.ID() != proposed.ID() {
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

func testPolicy() Policy {
	return Policy{
		RetryDispatchDelay:          time.Second,
		OutboxRetryBaseDelay:        time.Second,
		OutboxRetryMaxDelay:         5 * time.Minute,
		ContentSelectionWaitTimeout: 15 * time.Minute,
	}
}

func isInsufficientPrivilege(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "42501"
}
