ALTER TABLE catalog.outbox
    ADD COLUMN aggregate_id TEXT,
    ADD COLUMN sequence BIGINT;

DO $migration$
BEGIN
    IF EXISTS (
        SELECT legacy.event_id
        FROM catalog.outbox AS legacy
        LEFT JOIN catalog.books AS book ON book.created_at = legacy.occurred_at
        GROUP BY legacy.event_id
        HAVING COUNT(book.id) <> 1
    ) THEN
        RAISE EXCEPTION 'catalog legacy outbox rows cannot be mapped to exactly one book';
    END IF;
END
$migration$;

UPDATE catalog.outbox AS legacy
SET aggregate_id = book.id,
    sequence = 0
FROM catalog.books AS book
WHERE book.created_at = legacy.occurred_at;

ALTER TABLE catalog.outbox
    ALTER COLUMN aggregate_id SET NOT NULL,
    ALTER COLUMN sequence SET NOT NULL,
    ADD CONSTRAINT outbox_sequence_check CHECK (sequence >= 0);

DROP INDEX catalog.outbox_pending_idx;
CREATE UNIQUE INDEX outbox_aggregate_sequence_idx ON catalog.outbox (aggregate_id, sequence);
CREATE INDEX outbox_pending_idx ON catalog.outbox
    (next_attempt_at, occurred_at, aggregate_id, sequence, event_id)
    WHERE published_at IS NULL;

ALTER TABLE catalog.books
    ADD COLUMN processing_stage TEXT NOT NULL DEFAULT 'queued'
        CHECK (processing_stage IN ('queued', 'extracting', 'chunks_ready', 'failed')),
    ADD COLUMN processing_failure_category TEXT NOT NULL DEFAULT ''
        CHECK (processing_failure_category IN ('', 'encrypted_document', 'extraction_not_permitted',
            'malformed_document', 'unsupported_document', 'no_extractable_text',
            'resource_limit_exceeded', 'source_integrity_mismatch', 'processing_timeout',
            'dependency_unavailable', 'internal_processing_error')),
    ADD COLUMN processing_updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    ADD COLUMN processing_version BIGINT NOT NULL DEFAULT 1 CHECK (processing_version > 0);

UPDATE catalog.books
SET processing_stage = CASE processing_status
    WHEN 'pending' THEN 'queued'
    WHEN 'failed' THEN 'failed'
    ELSE 'chunks_ready'
END,
processing_updated_at = created_at;

CREATE TABLE catalog.processing_inbox (
    event_id TEXT PRIMARY KEY,
    event_type TEXT NOT NULL CHECK (event_type IN (
        'ingestion.book.processing-started.v1',
        'ingestion.book.chunks-ready.v1',
        'ingestion.book.processing-failed.v1'
    )),
    payload_sha256 BYTEA NOT NULL CHECK (octet_length(payload_sha256) = 32),
    processed_at TIMESTAMPTZ NOT NULL
);

ALTER TABLE catalog.outbox DROP CONSTRAINT outbox_event_type_check;
ALTER TABLE catalog.outbox ADD CONSTRAINT outbox_event_type_check CHECK (event_type IN (
    'catalog.book.uploaded.v1',
    'catalog.book.processing-status-changed.v1'
));
