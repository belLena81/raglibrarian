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
