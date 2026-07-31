-- Add the durable layout-selection wait/result workflow without rewriting existing jobs.
ALTER TABLE ingestion.jobs DROP CONSTRAINT IF EXISTS jobs_state_check;
ALTER TABLE ingestion.jobs ADD CONSTRAINT jobs_state_check CHECK (
    state IN ('queued','awaiting_selection','processing','retrying','completed','failed')
);

ALTER TABLE ingestion.outbox DROP CONSTRAINT IF EXISTS outbox_event_type_check;
ALTER TABLE ingestion.outbox ADD CONSTRAINT outbox_event_type_check CHECK (event_type IN (
    'ingestion.book.processing-started.v1',
    'ingestion.book.content-selection-requested.v1',
    'ingestion.book.chunks-ready.v1',
    'ingestion.book.processing-failed.v1',
    'ingestion.book.artifacts-deleted.v1'
));

ALTER TABLE ingestion.outbox DROP CONSTRAINT IF EXISTS outbox_aggregate_sequence_check;
ALTER TABLE ingestion.outbox ADD CONSTRAINT outbox_aggregate_sequence_check CHECK (
    aggregate_sequence IN (1,2,3)
);

CREATE TABLE IF NOT EXISTS ingestion.content_selection_inbox (
    event_id                  TEXT        PRIMARY KEY,
    request_id                TEXT        NOT NULL UNIQUE,
    job_id                    TEXT        NOT NULL UNIQUE REFERENCES ingestion.jobs(id) ON DELETE CASCADE,
    book_id                   TEXT        NOT NULL,
    lifecycle_version         BIGINT      NOT NULL CHECK (lifecycle_version > 0),
    payload_digest            BYTEA       NOT NULL CHECK (octet_length(payload_digest) = 32),
    payload                   BYTEA       NOT NULL CHECK (octet_length(payload) BETWEEN 1 AND 262144),
    source_sha256             BYTEA       NOT NULL CHECK (octet_length(source_sha256) = 32),
    processing_profile_digest BYTEA       NOT NULL CHECK (octet_length(processing_profile_digest) = 32),
    received_at               TIMESTAMPTZ NOT NULL,
    accepted_at               TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS content_selection_inbox_book_idx
    ON ingestion.content_selection_inbox (book_id, lifecycle_version);

GRANT SELECT, INSERT, UPDATE, DELETE ON ingestion.content_selection_inbox TO ingestion_runtime;
GRANT SELECT ON ingestion.content_selection_inbox TO ingestion_e2e;
