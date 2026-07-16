CREATE SCHEMA IF NOT EXISTS catalog;

CREATE TABLE catalog.books (
    id TEXT PRIMARY KEY,
    title TEXT NOT NULL,
    author TEXT NOT NULL,
    year INTEGER NOT NULL CHECK (year >= 0),
    tags TEXT[] NOT NULL,
    processing_status TEXT NOT NULL CHECK (processing_status IN ('pending', 'processing', 'indexed', 'failed', 'reindexing', 'deleting', 'deleted')),
    created_at TIMESTAMPTZ NOT NULL,
    object_reference TEXT NOT NULL UNIQUE,
    checksum BYTEA NOT NULL CHECK (octet_length(checksum) = 32),
    byte_size BIGINT NOT NULL CHECK (byte_size > 0),
    media_type TEXT NOT NULL CHECK (media_type = 'application/pdf'),
    actor_id TEXT NOT NULL
);

CREATE INDEX books_created_at_id_idx ON catalog.books (created_at, id);

CREATE TABLE catalog.outbox (
    event_id TEXT PRIMARY KEY,
    event_type TEXT NOT NULL CHECK (event_type = 'catalog.book.uploaded.v1'),
    payload BYTEA NOT NULL,
    occurred_at TIMESTAMPTZ NOT NULL,
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at TIMESTAMPTZ NOT NULL,
    leased_until TIMESTAMPTZ,
    published_at TIMESTAMPTZ
);

CREATE INDEX outbox_pending_idx ON catalog.outbox (next_attempt_at, event_id)
    WHERE published_at IS NULL;
