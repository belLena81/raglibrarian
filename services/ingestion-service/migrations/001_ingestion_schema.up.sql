CREATE SCHEMA IF NOT EXISTS ingestion;

CREATE TABLE ingestion.inbox (
    event_id TEXT PRIMARY KEY,
    payload_digest BYTEA NOT NULL CHECK (octet_length(payload_digest) = 32),
    payload BYTEA NOT NULL CHECK (octet_length(payload) BETWEEN 1 AND 262144),
    business_key TEXT NOT NULL,
    source_sha256 BYTEA NOT NULL CHECK (octet_length(source_sha256) = 32),
    processing_config_digest BYTEA NOT NULL CHECK (octet_length(processing_config_digest) = 32),
    received_at TIMESTAMPTZ NOT NULL,
    completed_at TIMESTAMPTZ,
    UNIQUE (business_key)
);

CREATE TABLE ingestion.jobs (
    id TEXT PRIMARY KEY,
    book_id TEXT NOT NULL,
    source_sha256 BYTEA NOT NULL CHECK (octet_length(source_sha256) = 32),
    processing_config_digest BYTEA NOT NULL CHECK (octet_length(processing_config_digest) = 32),
    state TEXT NOT NULL CHECK (state IN ('queued','processing','retrying','completed','failed')),
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    lease_owner TEXT,
    lease_expires_at TIMESTAMPTZ,
    next_attempt_at TIMESTAMPTZ,
    failure_category TEXT,
    manifest_reference TEXT,
    manifest_sha256 BYTEA CHECK (manifest_sha256 IS NULL OR octet_length(manifest_sha256) = 32),
    structure_version TEXT NOT NULL,
    maximum_tokens INTEGER NOT NULL CHECK (maximum_tokens > 0),
    overlap_tokens INTEGER NOT NULL CHECK (overlap_tokens >= 0 AND overlap_tokens < maximum_tokens),
    manifest_byte_size BIGINT CHECK (manifest_byte_size IS NULL OR manifest_byte_size > 0),
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    UNIQUE (book_id, source_sha256, processing_config_digest)
);

CREATE INDEX jobs_due_idx ON ingestion.jobs (next_attempt_at, id) WHERE state = 'retrying';
CREATE INDEX jobs_lease_idx ON ingestion.jobs (lease_expires_at, id) WHERE state = 'processing';

CREATE TABLE ingestion.retry_dispatches (
    job_id TEXT NOT NULL REFERENCES ingestion.jobs(id) ON DELETE CASCADE,
    attempt INTEGER NOT NULL CHECK (attempt > 0),
    event_id TEXT NOT NULL,
    payload BYTEA NOT NULL CHECK (octet_length(payload) BETWEEN 1 AND 262144),
    dispatch_after TIMESTAMPTZ NOT NULL,
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at TIMESTAMPTZ NOT NULL,
    leased_until TIMESTAMPTZ,
    published_at TIMESTAMPTZ,
    PRIMARY KEY (job_id, attempt)
);

CREATE INDEX retry_dispatches_pending_idx ON ingestion.retry_dispatches (next_attempt_at, job_id, attempt)
    WHERE published_at IS NULL;

CREATE TABLE ingestion.artifact_sets (
    job_id TEXT PRIMARY KEY REFERENCES ingestion.jobs(id) ON DELETE CASCADE,
    prefix TEXT NOT NULL UNIQUE,
    manifest_reference TEXT UNIQUE,
    manifest_sha256 BYTEA CHECK (manifest_sha256 IS NULL OR octet_length(manifest_sha256) = 32),
    structure_version TEXT NOT NULL,
    maximum_tokens INTEGER NOT NULL CHECK (maximum_tokens > 0),
    overlap_tokens INTEGER NOT NULL CHECK (overlap_tokens >= 0 AND overlap_tokens < maximum_tokens),
    committed_at TIMESTAMPTZ,
    cleanup_after TIMESTAMPTZ,
    cleanup_lease_until TIMESTAMPTZ,
    cleanup_attempts INTEGER NOT NULL DEFAULT 0 CHECK (cleanup_attempts >= 0),
    cleanup_completed_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE ingestion.outbox (
    event_id TEXT PRIMARY KEY,
    event_type TEXT NOT NULL CHECK (event_type IN ('ingestion.book.processing-started.v1','ingestion.book.chunks-ready.v1','ingestion.book.processing-failed.v1')),
    aggregate_id TEXT NOT NULL,
    aggregate_sequence SMALLINT NOT NULL CHECK (aggregate_sequence IN (1,2)),
    payload BYTEA NOT NULL,
    occurred_at TIMESTAMPTZ NOT NULL,
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at TIMESTAMPTZ NOT NULL,
    leased_until TIMESTAMPTZ,
    published_at TIMESTAMPTZ,
    UNIQUE (aggregate_id, aggregate_sequence)
);

CREATE INDEX ingestion_outbox_pending_idx ON ingestion.outbox (next_attempt_at, aggregate_id, aggregate_sequence)
    WHERE published_at IS NULL;

GRANT SELECT ON ingestion.artifact_sets TO ingestion_cleanup;
GRANT UPDATE (cleanup_after, cleanup_lease_until, cleanup_attempts, cleanup_completed_at, updated_at)
    ON ingestion.artifact_sets TO ingestion_cleanup;
