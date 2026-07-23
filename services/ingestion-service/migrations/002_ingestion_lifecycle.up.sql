ALTER TABLE ingestion.inbox
    ADD COLUMN lifecycle_version BIGINT NOT NULL DEFAULT 1 CHECK (lifecycle_version > 0);

ALTER TABLE ingestion.jobs
    ADD COLUMN lifecycle_version BIGINT NOT NULL DEFAULT 1 CHECK (lifecycle_version > 0);

ALTER TABLE ingestion.artifact_sets
    ADD COLUMN lifecycle_version BIGINT NOT NULL DEFAULT 1 CHECK (lifecycle_version > 0),
    ADD COLUMN deletion_event_id TEXT,
    ADD COLUMN deletion_cleanup_completed_at TIMESTAMPTZ;

CREATE TABLE ingestion.lifecycle_fences (
    book_id TEXT PRIMARY KEY,
    lifecycle_version BIGINT NOT NULL CHECK (lifecycle_version > 0),
    deleted BOOLEAN NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE ingestion.deletion_inbox (
    event_id TEXT PRIMARY KEY,
    book_id TEXT NOT NULL,
    command_id TEXT NOT NULL UNIQUE,
    lifecycle_version BIGINT NOT NULL CHECK (lifecycle_version > 0),
    payload_digest BYTEA NOT NULL CHECK (octet_length(payload_digest) = 32),
    ack_event_id TEXT NOT NULL UNIQUE,
    ack_event_type TEXT NOT NULL CHECK (ack_event_type = 'ingestion.book.artifacts-deleted.v1'),
    ack_payload BYTEA NOT NULL CHECK (octet_length(ack_payload) BETWEEN 1 AND 262144),
    ack_occurred_at TIMESTAMPTZ NOT NULL,
    occurred_at TIMESTAMPTZ NOT NULL,
    completed_at TIMESTAMPTZ,
    UNIQUE (book_id, lifecycle_version)
);

ALTER TABLE ingestion.artifact_sets
    ADD CONSTRAINT artifact_sets_deletion_event_fk
    FOREIGN KEY (deletion_event_id) REFERENCES ingestion.deletion_inbox(event_id);

CREATE INDEX artifact_sets_deletion_pending_idx
    ON ingestion.artifact_sets (deletion_event_id, cleanup_after, job_id)
    WHERE deletion_event_id IS NOT NULL AND deletion_cleanup_completed_at IS NULL;

ALTER TABLE ingestion.outbox DROP CONSTRAINT outbox_event_type_check;
ALTER TABLE ingestion.outbox ADD CONSTRAINT outbox_event_type_check CHECK (
    event_type IN (
        'ingestion.book.processing-started.v1',
        'ingestion.book.chunks-ready.v1',
        'ingestion.book.processing-failed.v1',
        'ingestion.book.artifacts-deleted.v1'
    )
);

GRANT SELECT (
    event_id,
    ack_event_id,
    ack_event_type,
    ack_payload,
    ack_occurred_at,
    completed_at
) ON ingestion.deletion_inbox TO ingestion_cleanup;
GRANT UPDATE (completed_at) ON ingestion.deletion_inbox TO ingestion_cleanup;
GRANT UPDATE (
    manifest_reference,
    manifest_sha256,
    deletion_cleanup_completed_at
) ON ingestion.artifact_sets TO ingestion_cleanup;
GRANT UPDATE (
    manifest_reference,
    manifest_sha256,
    manifest_byte_size,
    updated_at
) ON ingestion.jobs TO ingestion_cleanup;
GRANT SELECT (id) ON ingestion.jobs TO ingestion_cleanup;
GRANT INSERT (
    event_id,
    event_type,
    aggregate_id,
    aggregate_sequence,
    payload,
    occurred_at,
    next_attempt_at
) ON ingestion.outbox TO ingestion_cleanup;
