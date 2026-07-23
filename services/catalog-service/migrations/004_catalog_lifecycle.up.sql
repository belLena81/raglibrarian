ALTER TABLE catalog.books
    DROP CONSTRAINT books_media_type_check,
    ALTER COLUMN title DROP NOT NULL,
    ALTER COLUMN author DROP NOT NULL,
    ALTER COLUMN year DROP NOT NULL,
    ALTER COLUMN tags DROP NOT NULL,
    ALTER COLUMN object_reference DROP NOT NULL,
    ALTER COLUMN checksum DROP NOT NULL,
    ALTER COLUMN byte_size DROP NOT NULL,
    ALTER COLUMN media_type DROP NOT NULL,
    ALTER COLUMN actor_id DROP NOT NULL,
    ALTER COLUMN processing_stage DROP NOT NULL,
    ALTER COLUMN processing_failure_category DROP NOT NULL,
    ADD CONSTRAINT books_media_type_check CHECK (
        media_type IN ('application/pdf', 'application/epub+zip')
    ),
    ADD COLUMN lifecycle_version BIGINT NOT NULL DEFAULT 1 CHECK (lifecycle_version >= 1),
    ADD COLUMN manifest_reference TEXT,
    ADD COLUMN manifest_sha256 BYTEA,
    ADD COLUMN lifecycle_command_id TEXT NOT NULL DEFAULT '',
    ADD COLUMN original_deleted BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN artifacts_deleted BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN index_deleted BOOLEAN NOT NULL DEFAULT FALSE,
    ADD CONSTRAINT books_manifest_pair_check CHECK (
        (manifest_reference IS NULL AND manifest_sha256 IS NULL)
        OR (manifest_reference IS NOT NULL AND manifest_reference <> '' AND octet_length(manifest_sha256) = 32)
    );

UPDATE catalog.books
SET title=NULL,author=NULL,year=NULL,tags=NULL,object_reference=NULL,checksum=NULL,byte_size=NULL,
    media_type=NULL,actor_id=NULL,processing_stage=NULL,processing_failure_category=NULL,
    manifest_reference=NULL,manifest_sha256=NULL
WHERE processing_status='deleted';

DELETE FROM catalog.outbox
WHERE aggregate_id IN (SELECT id FROM catalog.books WHERE processing_status='deleted');

ALTER TABLE catalog.books
    ADD CONSTRAINT books_tombstone_shape_check CHECK (
        (processing_status = 'deleted'
            AND title IS NULL AND author IS NULL AND year IS NULL AND tags IS NULL
            AND object_reference IS NULL AND checksum IS NULL AND byte_size IS NULL
            AND media_type IS NULL AND actor_id IS NULL AND processing_stage IS NULL
            AND processing_failure_category IS NULL AND manifest_reference IS NULL
            AND manifest_sha256 IS NULL)
        OR
        (processing_status <> 'deleted'
            AND title IS NOT NULL AND author IS NOT NULL AND year IS NOT NULL AND tags IS NOT NULL
            AND object_reference IS NOT NULL AND checksum IS NOT NULL AND byte_size IS NOT NULL
            AND media_type IS NOT NULL AND actor_id IS NOT NULL AND processing_stage IS NOT NULL
            AND processing_failure_category IS NOT NULL)
    );

CREATE TABLE catalog.lifecycle_commands (
    command_id TEXT PRIMARY KEY,
    book_id TEXT NOT NULL REFERENCES catalog.books(id),
    command_type TEXT NOT NULL CHECK (command_type IN ('reindex', 'delete')),
    lifecycle_version BIGINT NOT NULL CHECK (lifecycle_version >= 2),
    actor_id TEXT,
    correlation_id TEXT,
    accepted_at TIMESTAMPTZ NOT NULL,
    UNIQUE (book_id, lifecycle_version)
);

CREATE TABLE catalog.lifecycle_inbox (
    event_id TEXT PRIMARY KEY,
    event_type TEXT NOT NULL CHECK (event_type IN (
        'ingestion.book.artifacts-deleted.v1',
        'retrieval.book.index-deleted.v1'
    )),
    payload_sha256 BYTEA NOT NULL CHECK (octet_length(payload_sha256) = 32),
    processed_at TIMESTAMPTZ NOT NULL
);

ALTER TABLE catalog.outbox DROP CONSTRAINT outbox_event_type_check;
ALTER TABLE catalog.outbox ADD CONSTRAINT outbox_event_type_check CHECK (event_type IN (
    'catalog.book.uploaded.v1',
    'catalog.book.processing-status-changed.v1',
    'catalog.book.reindex-requested.v1',
    'catalog.book.deletion-requested.v1'
));
