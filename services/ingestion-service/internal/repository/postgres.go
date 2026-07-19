// Package repository implements Ingestion's PostgreSQL persistence boundary.
package repository

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/application"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/artifact"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Postgres struct{ pool *pgxpool.Pool }

type PendingOutboxEvent struct {
	ID, Type, AggregateID string
	Payload               []byte
	Attempts              int
}

func NewPostgres(pool *pgxpool.Pool) *Postgres {
	if pool == nil {
		panic("ingestion repository: pool is required")
	}
	return &Postgres{pool: pool}
}

func (r *Postgres) Accept(ctx context.Context, event application.UploadedEvent, payloadDigest [32]byte, proposed domain.ProcessingJob, started application.OutboxEvent) (domain.ProcessingJob, bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return domain.ProcessingJob{}, false, fmt.Errorf("ingestion: begin accept: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	configBytes := configDigestBytes(proposed.ConfigDigest())
	command, err := tx.Exec(ctx, `INSERT INTO ingestion.inbox(event_id,payload_digest,business_key,source_sha256,processing_config_digest,received_at)
		VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING`, event.EventID, payloadDigest[:], event.IdempotencyKey, event.SourceSHA256[:], configBytes, proposed.CreatedAt())
	if err != nil {
		return domain.ProcessingJob{}, false, fmt.Errorf("ingestion: insert inbox: %w", err)
	}
	if command.RowsAffected() == 0 {
		var existingDigest, existingSource []byte
		var existingEventID string
		if err = tx.QueryRow(ctx, `SELECT event_id,payload_digest,source_sha256 FROM ingestion.inbox WHERE event_id=$1 OR business_key=$2 FOR UPDATE`, event.EventID, event.IdempotencyKey).Scan(&existingEventID, &existingDigest, &existingSource); err != nil {
			return domain.ProcessingJob{}, false, fmt.Errorf("ingestion: inspect duplicate: %w", err)
		}
		if (existingEventID == event.EventID && !constantEqual(existingDigest, payloadDigest[:])) || !constantEqual(existingSource, event.SourceSHA256[:]) {
			return domain.ProcessingJob{}, false, application.ErrConflictingEvent
		}
		existingJob, loadErr := loadJobForUpdate(ctx, tx, event.BookID, event.SourceSHA256, proposed.ConfigDigest())
		if errors.Is(loadErr, pgx.ErrNoRows) {
			return domain.ProcessingJob{}, false, application.ErrConflictingEvent
		}
		if loadErr != nil {
			return domain.ProcessingJob{}, false, loadErr
		}
		now := proposed.UpdatedAt()
		claimable, decisionErr := existingJobDecision(existingJob, now)
		if !claimable {
			return existingJob, false, decisionErr
		}
		lease := proposed.LeaseExpiresAt().Sub(now)
		if err = existingJob.Claim(proposed.LeaseOwner(), now, lease); err != nil {
			return domain.ProcessingJob{}, false, err
		}
		_, err = tx.Exec(ctx, `UPDATE ingestion.jobs SET state='processing',attempts=$2,lease_owner=$3,lease_expires_at=$4,next_attempt_at=NULL,updated_at=$5 WHERE id=$1`, existingJob.ID(), existingJob.Attempts(), existingJob.LeaseOwner(), existingJob.LeaseExpiresAt(), existingJob.UpdatedAt())
		if err != nil {
			return domain.ProcessingJob{}, false, fmt.Errorf("ingestion: reclaim job: %w", err)
		}
		if err = tx.Commit(ctx); err != nil {
			return domain.ProcessingJob{}, false, fmt.Errorf("ingestion: commit reclaim: %w", err)
		}
		return existingJob, true, nil
	}
	sourceSHA256 := proposed.SourceSHA256()
	command, err = tx.Exec(ctx, `INSERT INTO ingestion.jobs
        (id,book_id,source_sha256,processing_config_digest,state,attempts,lease_owner,lease_expires_at,created_at,updated_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT(book_id,source_sha256,processing_config_digest) DO NOTHING`, proposed.ID(), proposed.BookID(), sourceSHA256[:], configDigestBytes(proposed.ConfigDigest()), proposed.State(), proposed.Attempts(), proposed.LeaseOwner(), proposed.LeaseExpiresAt(), proposed.CreatedAt(), proposed.UpdatedAt())
	if err != nil {
		return domain.ProcessingJob{}, false, fmt.Errorf("ingestion: insert job: %w", err)
	}
	if command.RowsAffected() == 0 {
		if err = tx.Commit(ctx); err != nil {
			return domain.ProcessingJob{}, false, fmt.Errorf("ingestion: commit duplicate: %w", err)
		}
		return proposed, false, nil
	}
	_, err = tx.Exec(ctx, `INSERT INTO ingestion.outbox(event_id,event_type,aggregate_id,aggregate_sequence,payload,occurred_at,next_attempt_at)
		VALUES($1,$2,$3,1,$4,$5,$5) ON CONFLICT(aggregate_id,aggregate_sequence) DO NOTHING`, started.ID, started.Type, proposed.ID(), started.Payload, started.OccurredAt)
	if err != nil {
		return domain.ProcessingJob{}, false, fmt.Errorf("ingestion: insert started outbox: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.ProcessingJob{}, false, fmt.Errorf("ingestion: commit accept: %w", err)
	}
	return proposed, true, nil
}

func existingJobDecision(job domain.ProcessingJob, now time.Time) (bool, error) {
	if job.State() == domain.JobRetrying && now.Before(job.NextAttemptAt()) {
		return false, application.NewDeferredError(job.NextAttemptAt())
	}
	if job.State() == domain.JobProcessing && now.Before(job.LeaseExpiresAt()) {
		return false, application.NewDeferredError(job.LeaseExpiresAt())
	}
	if job.State() == domain.JobCompleted || job.State() == domain.JobFailed {
		return false, nil
	}
	return true, nil
}

func loadJobForUpdate(ctx context.Context, tx pgx.Tx, bookID string, sourceSHA256 [32]byte, configDigest string) (domain.ProcessingJob, error) {
	var id, state, leaseOwner, failure, manifestReference string
	var source, manifestSHA []byte
	var attempts int
	var leaseExpiresAt, nextAttemptAt *time.Time
	var manifestSize *int64
	var createdAt, updatedAt time.Time
	err := tx.QueryRow(ctx, `SELECT id,state,attempts,COALESCE(lease_owner,''),lease_expires_at,next_attempt_at,COALESCE(failure_category,''),COALESCE(manifest_reference,''),manifest_sha256,manifest_byte_size,created_at,updated_at,source_sha256
	    FROM ingestion.jobs WHERE book_id=$1 AND source_sha256=$2 AND processing_config_digest=$3 FOR UPDATE`, bookID, sourceSHA256[:], configDigestBytes(configDigest)).Scan(&id, &state, &attempts, &leaseOwner, &leaseExpiresAt, &nextAttemptAt, &failure, &manifestReference, &manifestSHA, &manifestSize, &createdAt, &updatedAt, &source)
	if err != nil {
		return domain.ProcessingJob{}, err
	}
	var sourceSum, manifestSum [32]byte
	copy(sourceSum[:], source)
	copy(manifestSum[:], manifestSHA)
	var leaseTime, nextTime time.Time
	if leaseExpiresAt != nil {
		leaseTime = *leaseExpiresAt
	}
	if nextAttemptAt != nil {
		nextTime = *nextAttemptAt
	}
	var size int64
	if manifestSize != nil {
		size = *manifestSize
	}
	return domain.RestoreProcessingJob(id, bookID, sourceSum, configDigest, domain.JobState(state), attempts, leaseOwner, leaseTime, nextTime, domain.FailureCategory(failure), manifestReference, manifestSum, size, createdAt, updatedAt)
}

func (r *Postgres) Complete(ctx context.Context, job domain.ProcessingJob, claim application.ClaimToken, result artifact.Result, ready application.OutboxEvent) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("ingestion: begin complete: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	command, err := tx.Exec(ctx, `UPDATE ingestion.jobs SET state='completed',lease_owner=NULL,lease_expires_at=NULL,
		manifest_reference=$2,manifest_sha256=$3,manifest_byte_size=$4,updated_at=$5
		WHERE id=$1 AND state='processing' AND lease_owner=$6 AND attempts=$7 AND lease_expires_at=$8 AND lease_expires_at >= $5`, job.ID(), result.ManifestReference, result.ManifestSHA256[:], result.ManifestByteSize, job.UpdatedAt(), claim.Owner, claim.Attempt, claim.ExpiresAt)
	if err != nil {
		return fmt.Errorf("ingestion: update completed job: %w", err)
	}
	if command.RowsAffected() == 0 {
		return domain.ErrLeaseNotOwned
	}
	prefix := strings.TrimSuffix(result.ManifestReference, "manifest.pb")
	if prefix == result.ManifestReference {
		return errors.New("ingestion: invalid manifest reference")
	}
	_, err = tx.Exec(ctx, `INSERT INTO ingestion.artifact_sets(job_id,prefix,manifest_reference,manifest_sha256,committed_at,updated_at)
        VALUES($1,$2,$3,$4,$5,$5)
        ON CONFLICT(job_id) DO UPDATE SET manifest_reference=EXCLUDED.manifest_reference,manifest_sha256=EXCLUDED.manifest_sha256,committed_at=EXCLUDED.committed_at,updated_at=EXCLUDED.updated_at`, job.ID(), prefix, result.ManifestReference, result.ManifestSHA256[:], job.UpdatedAt())
	if err != nil {
		return fmt.Errorf("ingestion: commit artifact set: %w", err)
	}
	if err = r.insertTerminalOutbox(ctx, tx, job.ID(), ready); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE ingestion.inbox SET completed_at=$2 WHERE business_key=$1 AND completed_at IS NULL`, job.BookID(), job.UpdatedAt())
	if err != nil {
		return fmt.Errorf("ingestion: complete inbox: %w", err)
	}
	return tx.Commit(ctx)
}

func (r *Postgres) Fail(ctx context.Context, job domain.ProcessingJob, claim application.ClaimToken, failed application.OutboxEvent) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("ingestion: begin fail: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	command, err := tx.Exec(ctx, `UPDATE ingestion.jobs SET state='failed',lease_owner=NULL,lease_expires_at=NULL,failure_category=$2,updated_at=$3
	    WHERE id=$1 AND state='processing' AND lease_owner=$4 AND attempts=$5 AND lease_expires_at=$6 AND lease_expires_at >= $3`, job.ID(), job.Failure(), job.UpdatedAt(), claim.Owner, claim.Attempt, claim.ExpiresAt)
	if err != nil {
		return fmt.Errorf("ingestion: update failed job: %w", err)
	}
	if command.RowsAffected() == 0 {
		return domain.ErrLeaseNotOwned
	}
	if err = r.insertTerminalOutbox(ctx, tx, job.ID(), failed); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *Postgres) Retry(ctx context.Context, job domain.ProcessingJob, claim application.ClaimToken) error {
	command, err := r.pool.Exec(ctx, `UPDATE ingestion.jobs SET state='retrying',lease_owner=NULL,lease_expires_at=NULL,next_attempt_at=$2,updated_at=$3
	    WHERE id=$1 AND state='processing' AND lease_owner=$4 AND attempts=$5 AND lease_expires_at=$6 AND lease_expires_at >= $3`, job.ID(), job.NextAttemptAt(), job.UpdatedAt(), claim.Owner, claim.Attempt, claim.ExpiresAt)
	if err != nil {
		return fmt.Errorf("ingestion: schedule retry: %w", err)
	}
	if command.RowsAffected() == 0 {
		return domain.ErrLeaseNotOwned
	}
	return nil
}

func (r *Postgres) insertTerminalOutbox(ctx context.Context, tx pgx.Tx, jobID string, event application.OutboxEvent) error {
	_, err := tx.Exec(ctx, `INSERT INTO ingestion.outbox(event_id,event_type,aggregate_id,aggregate_sequence,payload,occurred_at,next_attempt_at)
        VALUES($1,$2,$3,2,$4,$5,$5) ON CONFLICT(aggregate_id,aggregate_sequence) DO NOTHING`, event.ID, event.Type, jobID, event.Payload, event.OccurredAt)
	if err != nil {
		return fmt.Errorf("ingestion: insert terminal outbox: %w", err)
	}
	return nil
}

func (r *Postgres) ClaimOutbox(ctx context.Context, now time.Time, lease time.Duration) ([]PendingOutboxEvent, error) {
	rows, err := r.pool.Query(ctx, `WITH candidates AS (
        SELECT event_id FROM ingestion.outbox o WHERE published_at IS NULL AND next_attempt_at <= $1
        AND (leased_until IS NULL OR leased_until < $1)
        AND NOT EXISTS (SELECT 1 FROM ingestion.outbox prior WHERE prior.aggregate_id=o.aggregate_id AND prior.aggregate_sequence<o.aggregate_sequence AND prior.published_at IS NULL)
        ORDER BY next_attempt_at,aggregate_id,aggregate_sequence FOR UPDATE SKIP LOCKED LIMIT 1)
        UPDATE ingestion.outbox o SET leased_until=$2 FROM candidates WHERE o.event_id=candidates.event_id
        RETURNING o.event_id,o.event_type,o.aggregate_id,o.payload,o.attempts`, now, now.Add(lease))
	if err != nil {
		return nil, fmt.Errorf("ingestion: claim outbox: %w", err)
	}
	defer rows.Close()
	var result []PendingOutboxEvent
	for rows.Next() {
		var value PendingOutboxEvent
		if err = rows.Scan(&value.ID, &value.Type, &value.AggregateID, &value.Payload, &value.Attempts); err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func (r *Postgres) MarkPublished(ctx context.Context, id string, now time.Time) error {
	_, err := r.pool.Exec(ctx, `UPDATE ingestion.outbox SET published_at=$2,leased_until=NULL WHERE event_id=$1`, id, now)
	return err
}

func (r *Postgres) RetryOutbox(ctx context.Context, id string, now time.Time, attempt int) error {
	delay := time.Second << min(attempt, 8)
	if delay > 5*time.Minute {
		delay = 5 * time.Minute
	}
	_, err := r.pool.Exec(ctx, `UPDATE ingestion.outbox SET attempts=attempts+1,next_attempt_at=$2,leased_until=NULL WHERE event_id=$1`, id, now.Add(delay))
	return err
}

func (r *Postgres) OrphanPrefixes(ctx context.Context, updatedBefore time.Time, limit int) ([]string, error) {
	rows, err := r.pool.Query(ctx, `SELECT 'books/' || book_id || '/' || encode(source_sha256,'hex') || '/' || encode(processing_config_digest,'hex') || '/'
	    FROM ingestion.jobs WHERE state = 'failed' AND updated_at < $1 ORDER BY updated_at,id LIMIT $2`, updatedBefore, limit)
	if err != nil {
		return nil, fmt.Errorf("ingestion: list orphan prefixes: %w", err)
	}
	defer rows.Close()
	var prefixes []string
	for rows.Next() {
		var prefix string
		if err = rows.Scan(&prefix); err != nil {
			return nil, fmt.Errorf("ingestion: scan orphan prefix: %w", err)
		}
		prefixes = append(prefixes, prefix)
	}
	return prefixes, rows.Err()
}

func constantEqual(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	var diff byte
	for index := range left {
		diff |= left[index] ^ right[index]
	}
	return diff == 0
}

func configDigestBytes(value string) []byte {
	decoded, _ := hex.DecodeString(value)
	return decoded
}
