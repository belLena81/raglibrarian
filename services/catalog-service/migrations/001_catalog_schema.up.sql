CREATE SCHEMA IF NOT EXISTS catalog AUTHORIZATION catalog_migrator;

CREATE TABLE IF NOT EXISTS catalog.books (
    id                        TEXT        PRIMARY KEY,
    title                     TEXT,
    author                    TEXT,
    year                      INTEGER,
    tags                      TEXT[],
    processing_status         TEXT        NOT NULL CHECK (processing_status IN ('pending', 'processing', 'indexed', 'failed', 'reindexing', 'deleting', 'deleted')),
    created_at                TIMESTAMPTZ NOT NULL,
    object_reference          TEXT UNIQUE,
    checksum                  BYTEA       CHECK (checksum IS NULL OR octet_length(checksum) = 32),
    byte_size                 BIGINT      CHECK (byte_size IS NULL OR byte_size > 0),
    media_type                TEXT,
    actor_id                  TEXT,
    processing_stage          TEXT        DEFAULT 'queued' CHECK (processing_stage IN ('queued', 'extracting', 'chunks_ready', 'indexed', 'failed')),
    processing_failure_category TEXT      DEFAULT '' CHECK (processing_failure_category IN ('', 'encrypted_document', 'extraction_not_permitted',
        'malformed_document', 'unsupported_document', 'no_extractable_text',
        'resource_limit_exceeded', 'source_integrity_mismatch', 'processing_timeout',
        'dependency_unavailable', 'internal_processing_error', 'manifest_integrity',
        'incompatible_profile', 'embedding_unavailable', 'vector_store_unavailable',
        'indexing_timeout', 'internal_indexing_error')),
    processing_failure_detail   TEXT      NOT NULL DEFAULT '' CHECK (
        processing_failure_detail ~ '^[a-z0-9_-]{0,128}$'
    ),
    processing_updated_at      TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    processing_version         BIGINT      NOT NULL DEFAULT 1 CHECK (processing_version > 0),
    lifecycle_version          BIGINT      NOT NULL DEFAULT 1 CHECK (lifecycle_version >= 1),
    manifest_reference         TEXT,
    manifest_sha256            BYTEA,
    lifecycle_command_id       TEXT        NOT NULL DEFAULT '',
    original_deleted           BOOLEAN     NOT NULL DEFAULT FALSE,
    artifacts_deleted          BOOLEAN     NOT NULL DEFAULT FALSE,
    index_deleted              BOOLEAN     NOT NULL DEFAULT FALSE,

    CONSTRAINT books_manifest_pair_check CHECK (
        (manifest_reference IS NULL AND manifest_sha256 IS NULL)
        OR (manifest_reference IS NOT NULL AND manifest_reference <> '' AND octet_length(manifest_sha256) = 32)
    ),
    CONSTRAINT books_media_type_check CHECK (
        media_type IS NULL OR media_type IN ('application/pdf', 'application/epub+zip')
    ),
    CONSTRAINT books_tombstone_shape_check CHECK (
        (processing_status = 'deleted'
            AND title IS NULL AND author IS NULL AND year IS NULL AND tags IS NULL
            AND object_reference IS NULL AND checksum IS NULL AND byte_size IS NULL
            AND media_type IS NULL AND actor_id IS NULL AND processing_stage IS NULL
            AND processing_failure_category IS NULL AND processing_failure_detail = '' AND manifest_reference IS NULL
            AND manifest_sha256 IS NULL)
        OR
        (processing_status <> 'deleted'
            AND title IS NOT NULL AND author IS NOT NULL AND year IS NOT NULL AND tags IS NOT NULL
            AND object_reference IS NOT NULL AND checksum IS NOT NULL AND byte_size IS NOT NULL
            AND media_type IS NOT NULL AND actor_id IS NOT NULL AND processing_stage IS NOT NULL
            AND processing_failure_category IS NOT NULL AND processing_failure_detail IS NOT NULL)
    )
);

CREATE INDEX IF NOT EXISTS books_created_at_id_idx
    ON catalog.books (created_at, id);

CREATE TABLE IF NOT EXISTS catalog.outbox (
    event_id       TEXT        PRIMARY KEY,
    event_type     TEXT        NOT NULL CHECK (event_type IN (
        'catalog.book.uploaded.v1',
        'catalog.book.processing-status-changed.v1',
        'catalog.book.reindex-requested.v1',
        'catalog.book.deletion-requested.v1'
    )),
    payload        BYTEA       NOT NULL,
    occurred_at    TIMESTAMPTZ NOT NULL,
    attempts       INTEGER     NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at TIMESTAMPTZ NOT NULL,
    leased_until   TIMESTAMPTZ,
    published_at   TIMESTAMPTZ,
    aggregate_id   TEXT        NOT NULL,
    sequence       BIGINT      NOT NULL,

    CONSTRAINT outbox_sequence_check CHECK (sequence >= 0)
);

CREATE UNIQUE INDEX IF NOT EXISTS outbox_aggregate_sequence_idx
    ON catalog.outbox (aggregate_id, sequence);

CREATE INDEX IF NOT EXISTS outbox_pending_idx ON catalog.outbox
    (next_attempt_at, occurred_at, aggregate_id, sequence, event_id)
    WHERE published_at IS NULL;

CREATE TABLE IF NOT EXISTS catalog.processing_inbox (
    event_id      TEXT        PRIMARY KEY,
    event_type    TEXT        NOT NULL CHECK (event_type IN (
        'ingestion.book.processing-started.v1',
        'ingestion.book.chunks-ready.v1',
        'ingestion.book.processing-failed.v1',
        'retrieval.book.indexed.v1',
        'retrieval.book.indexing-failed.v1'
    )),
    payload_sha256 BYTEA      NOT NULL CHECK (octet_length(payload_sha256) = 32),
    processed_at  TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS catalog.lifecycle_commands (
    command_id         TEXT        PRIMARY KEY,
    book_id            TEXT        NOT NULL REFERENCES catalog.books(id),
    command_type       TEXT        NOT NULL CHECK (command_type IN ('reindex', 'delete')),
    lifecycle_version   BIGINT      NOT NULL CHECK (lifecycle_version >= 2),
    actor_id           TEXT,
    correlation_id     TEXT,
    accepted_at        TIMESTAMPTZ NOT NULL,
    UNIQUE (book_id, lifecycle_version)
);

CREATE TABLE IF NOT EXISTS catalog.lifecycle_inbox (
    event_id      TEXT        PRIMARY KEY,
    event_type    TEXT        NOT NULL CHECK (event_type IN (
        'ingestion.book.artifacts-deleted.v1',
        'retrieval.book.index-deleted.v1'
    )),
    payload_sha256 BYTEA      NOT NULL CHECK (octet_length(payload_sha256) = 32),
    processed_at  TIMESTAMPTZ NOT NULL
);

GRANT USAGE ON SCHEMA catalog TO catalog_runtime;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA catalog TO catalog_runtime;
