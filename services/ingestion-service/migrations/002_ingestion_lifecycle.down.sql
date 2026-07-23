REVOKE INSERT (
    event_id,
    event_type,
    aggregate_id,
    aggregate_sequence,
    payload,
    occurred_at,
    next_attempt_at
) ON ingestion.outbox FROM ingestion_cleanup;
REVOKE SELECT (id) ON ingestion.jobs FROM ingestion_cleanup;
REVOKE UPDATE (
    manifest_reference,
    manifest_sha256,
    manifest_byte_size,
    updated_at
) ON ingestion.jobs FROM ingestion_cleanup;
REVOKE UPDATE (
    manifest_reference,
    manifest_sha256,
    deletion_cleanup_completed_at
) ON ingestion.artifact_sets FROM ingestion_cleanup;

ALTER TABLE ingestion.outbox DROP CONSTRAINT outbox_event_type_check;
DELETE FROM ingestion.outbox WHERE event_type = 'ingestion.book.artifacts-deleted.v1';
ALTER TABLE ingestion.outbox ADD CONSTRAINT outbox_event_type_check CHECK (
    event_type IN (
        'ingestion.book.processing-started.v1',
        'ingestion.book.chunks-ready.v1',
        'ingestion.book.processing-failed.v1'
    )
);

DROP INDEX IF EXISTS ingestion.artifact_sets_deletion_pending_idx;
ALTER TABLE ingestion.artifact_sets DROP CONSTRAINT IF EXISTS artifact_sets_deletion_event_fk;
DROP TABLE IF EXISTS ingestion.deletion_inbox;
DROP TABLE IF EXISTS ingestion.lifecycle_fences;
ALTER TABLE ingestion.artifact_sets
    DROP COLUMN IF EXISTS deletion_cleanup_completed_at,
    DROP COLUMN IF EXISTS deletion_event_id,
    DROP COLUMN IF EXISTS lifecycle_version;
ALTER TABLE ingestion.jobs DROP COLUMN IF EXISTS lifecycle_version;
ALTER TABLE ingestion.inbox DROP COLUMN IF EXISTS lifecycle_version;
