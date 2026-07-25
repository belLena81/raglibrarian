-- Bootstrap schema for the first fresh app launch.
-- This release intentionally ships create-only DDL; upgrade migrations are out of scope.
CREATE SCHEMA IF NOT EXISTS retrieval AUTHORIZATION retrieval_migrator;

CREATE TABLE IF NOT EXISTS retrieval.metadata_facts (
    book_id          TEXT        PRIMARY KEY,
    event_id         TEXT        NOT NULL UNIQUE,
    payload_digest   BYTEA       NOT NULL CHECK (octet_length(payload_digest) = 32),
    source_sha256    BYTEA       NOT NULL CHECK (octet_length(source_sha256) = 32),
    title            TEXT        NOT NULL,
    author           TEXT        NOT NULL,
    publication_year INTEGER     NOT NULL CHECK (publication_year >= 0),
    tags             TEXT[]      NOT NULL DEFAULT '{}',
    correlation_id   TEXT        NOT NULL,
    causation_id     TEXT        NOT NULL,
    occurred_at      TIMESTAMPTZ NOT NULL,
    received_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    media_type       TEXT        NOT NULL DEFAULT 'application/pdf'
        CHECK (media_type IN ('application/pdf', 'application/epub+zip'))
);

CREATE TABLE IF NOT EXISTS retrieval.manifest_facts (
    book_id            TEXT        PRIMARY KEY,
    event_id           TEXT        NOT NULL UNIQUE,
    payload_digest     BYTEA       NOT NULL CHECK (octet_length(payload_digest) = 32),
    source_sha256      BYTEA       NOT NULL CHECK (octet_length(source_sha256) = 32),
    manifest_sha256    BYTEA       NOT NULL CHECK (octet_length(manifest_sha256) = 32),
    manifest_reference TEXT        NOT NULL,
    manifest_payload   BYTEA       NOT NULL,
    correlation_id     TEXT        NOT NULL,
    causation_id       TEXT        NOT NULL,
    occurred_at        TIMESTAMPTZ NOT NULL,
    received_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    failure_category   TEXT,
    failure_recorded_at TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS retrieval.index_jobs (
    id                           TEXT        PRIMARY KEY,
    book_id                      TEXT        NOT NULL REFERENCES retrieval.metadata_facts(book_id),
    source_sha256                BYTEA       NOT NULL CHECK (octet_length(source_sha256) = 32),
    manifest_sha256              BYTEA       NOT NULL CHECK (octet_length(manifest_sha256) = 32),
    profile_digest               BYTEA       NOT NULL CHECK (octet_length(profile_digest) = 32),
    state                        TEXT        NOT NULL CHECK (state IN ('pending','indexed','failed')),
    expected_batches             INTEGER     NOT NULL CHECK (expected_batches >= 0),
    evidence_count               INTEGER     NOT NULL DEFAULT 0 CHECK (evidence_count >= 0),
    failure_category             TEXT,
    correlation_id               TEXT        NOT NULL,
    created_at                   TIMESTAMPTZ NOT NULL,
    updated_at                   TIMESTAMPTZ NOT NULL,
    lifecycle_version            BIGINT      NOT NULL DEFAULT 1 CHECK (lifecycle_version >= 1),
    finalization_inflight        BOOLEAN     NOT NULL DEFAULT FALSE,
    finalization_lease_expires_at TIMESTAMPTZ,
    vector_cleanup_pending       BOOLEAN     NOT NULL DEFAULT FALSE,
    vector_cleanup_attempts      INTEGER     NOT NULL DEFAULT 0 CHECK (vector_cleanup_attempts >= 0),
    vector_cleanup_next_attempt_at TIMESTAMPTZ,
    CONSTRAINT index_jobs_book_id_source_sha256_manifest_sha256_profile_digest_key
        UNIQUE (book_id, source_sha256, manifest_sha256, profile_digest)
);

CREATE TABLE IF NOT EXISTS retrieval.document_embedding_accumulators (
    job_id       TEXT        PRIMARY KEY REFERENCES retrieval.index_jobs(id) ON DELETE CASCADE,
    vector_sum   REAL[]      NOT NULL CHECK (array_length(vector_sum, 1) = 768),
    chunk_count  INTEGER     NOT NULL CHECK (chunk_count > 0),
    updated_at   TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS retrieval.documents (
    document_id        TEXT        PRIMARY KEY,
    job_id             TEXT        NOT NULL UNIQUE REFERENCES retrieval.index_jobs(id) ON DELETE CASCADE,
    book_id            TEXT        NOT NULL,
    title              TEXT        NOT NULL,
    author             TEXT        NOT NULL,
    publication_year   INTEGER     NOT NULL,
    tags               TEXT[]      NOT NULL,
    chunk_count        INTEGER     NOT NULL CHECK (chunk_count > 0),
    page_start         INTEGER     NOT NULL CHECK (page_start >= 0),
    page_end           INTEGER     NOT NULL CHECK (page_end >= page_start),
    created_at         TIMESTAMPTZ NOT NULL,
    updated_at         TIMESTAMPTZ NOT NULL,
    media_type         TEXT        NOT NULL DEFAULT 'application/pdf'
        CHECK (media_type IN ('application/pdf', 'application/epub+zip'))
);

CREATE TABLE IF NOT EXISTS retrieval.index_batches (
    id                   TEXT        PRIMARY KEY,
    job_id               TEXT        NOT NULL REFERENCES retrieval.index_jobs(id) ON DELETE CASCADE,
    shard_reference      TEXT        NOT NULL,
    shard_sha256         BYTEA       NOT NULL CHECK (octet_length(shard_sha256) = 32),
    compressed_byte_size  BIGINT      NOT NULL CHECK (compressed_byte_size > 0),
    uncompressed_byte_size BIGINT     NOT NULL CHECK (uncompressed_byte_size > 0),
    chunk_count          INTEGER     NOT NULL CHECK (chunk_count > 0),
    state                TEXT        NOT NULL DEFAULT 'pending' CHECK (state IN ('pending','processing','completed','failed')),
    attempts             INTEGER     NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    lease_owner          TEXT,
    lease_expires_at     TIMESTAMPTZ,
    next_attempt_at      TIMESTAMPTZ,
    updated_at           TIMESTAMPTZ NOT NULL,
    UNIQUE (job_id, shard_reference)
);

CREATE TABLE IF NOT EXISTS retrieval.evidence (
    evidence_id      TEXT        PRIMARY KEY,
    chunk_id         TEXT        NOT NULL,
    job_id           TEXT        NOT NULL REFERENCES retrieval.index_jobs(id) ON DELETE CASCADE,
    book_id          TEXT        NOT NULL,
    title            TEXT        NOT NULL,
    author           TEXT        NOT NULL,
    publication_year INTEGER     NOT NULL,
    tags             TEXT[]      NOT NULL,
    chapter          TEXT        NOT NULL DEFAULT '',
    section          TEXT        NOT NULL DEFAULT '',
    page_start       INTEGER     NOT NULL CHECK (page_start >= 0),
    page_end         INTEGER     NOT NULL CHECK (page_end >= page_start),
    passage          TEXT        NOT NULL,
    content_sha256   BYTEA       NOT NULL CHECK (octet_length(content_sha256) = 32),
    created_at       TIMESTAMPTZ NOT NULL,
    media_type       TEXT        NOT NULL DEFAULT 'application/pdf'
        CHECK (media_type IN ('application/pdf', 'application/epub+zip')),
    UNIQUE (job_id, chunk_id)
);

CREATE TABLE IF NOT EXISTS retrieval.book_lifecycle (
    book_id                   TEXT        PRIMARY KEY REFERENCES retrieval.metadata_facts(book_id),
    lifecycle_version         BIGINT      NOT NULL CHECK (lifecycle_version >= 1),
    state                     TEXT        NOT NULL CHECK (state IN ('active','reindexing','deleting','deleted')),
    active_job_id             TEXT        REFERENCES retrieval.index_jobs(id),
    event_id                  TEXT        NOT NULL UNIQUE,
    command_id                TEXT,
    event_type                TEXT        NOT NULL,
    payload_digest            BYTEA       NOT NULL CHECK (octet_length(payload_digest) = 32),
    cleanup_pending           BOOLEAN     NOT NULL DEFAULT FALSE,
    cleanup_attempts          INTEGER     NOT NULL DEFAULT 0 CHECK (cleanup_attempts >= 0),
    cleanup_next_attempt_at   TIMESTAMPTZ,
    correlation_id            TEXT        NOT NULL,
    updated_at                TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS retrieval_book_lifecycle_cleanup_idx
    ON retrieval.book_lifecycle (cleanup_next_attempt_at)
    WHERE cleanup_pending;

CREATE TABLE IF NOT EXISTS retrieval.outbox (
    event_id        TEXT        PRIMARY KEY,
    event_type      TEXT        NOT NULL,
    aggregate_id    TEXT        NOT NULL,
    payload         BYTEA       NOT NULL,
    occurred_at     TIMESTAMPTZ NOT NULL,
    published_at    TIMESTAMPTZ,
    attempts        INTEGER     NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS retrieval_outbox_pending_idx
    ON retrieval.outbox (next_attempt_at)
    WHERE published_at IS NULL;

CREATE INDEX IF NOT EXISTS retrieval_index_jobs_vector_cleanup_idx
    ON retrieval.index_jobs (vector_cleanup_next_attempt_at)
    WHERE vector_cleanup_pending;

REVOKE ALL ON SCHEMA retrieval FROM PUBLIC;
REVOKE ALL ON ALL TABLES IN SCHEMA retrieval FROM PUBLIC;

GRANT USAGE ON SCHEMA retrieval TO retrieval_runtime, retrieval_search, retrieval_planner, retrieval_indexer, retrieval_dispatcher, retrieval_cleanup, retrieval_e2e;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA retrieval TO retrieval_runtime;
GRANT SELECT ON retrieval.index_jobs TO retrieval_search;
GRANT SELECT ON retrieval.documents TO retrieval_search;
GRANT SELECT ON ALL TABLES IN SCHEMA retrieval TO retrieval_e2e;
GRANT SELECT, INSERT, UPDATE ON retrieval.metadata_facts, retrieval.manifest_facts, retrieval.index_jobs, retrieval.index_batches, retrieval.outbox TO retrieval_planner;
GRANT DELETE ON retrieval.manifest_facts, retrieval.index_jobs, retrieval.outbox TO retrieval_planner;
GRANT SELECT, INSERT, UPDATE ON retrieval.index_jobs, retrieval.index_batches, retrieval.evidence, retrieval.outbox TO retrieval_indexer;
GRANT SELECT, INSERT, UPDATE ON retrieval.document_embedding_accumulators, retrieval.documents TO retrieval_indexer;
GRANT SELECT ON retrieval.metadata_facts TO retrieval_indexer;
GRANT SELECT ON retrieval.manifest_facts TO retrieval_indexer;
GRANT DELETE ON retrieval.index_jobs, retrieval.outbox TO retrieval_indexer;
GRANT SELECT, UPDATE ON retrieval.outbox TO retrieval_dispatcher;
GRANT SELECT, UPDATE ON retrieval.index_batches, retrieval.outbox TO retrieval_cleanup;
GRANT SELECT, UPDATE ON retrieval.index_jobs TO retrieval_cleanup;
GRANT SELECT, INSERT, UPDATE ON retrieval.book_lifecycle TO retrieval_runtime;
REVOKE DELETE ON retrieval.book_lifecycle FROM retrieval_runtime;
GRANT SELECT ON retrieval.book_lifecycle TO retrieval_search;
GRANT SELECT, INSERT, UPDATE ON retrieval.book_lifecycle TO retrieval_planner;
GRANT SELECT, UPDATE ON retrieval.book_lifecycle TO retrieval_indexer, retrieval_cleanup;
GRANT SELECT (book_id), UPDATE (title,author,publication_year,tags) ON retrieval.metadata_facts TO retrieval_cleanup;
GRANT SELECT (book_id), DELETE ON retrieval.manifest_facts TO retrieval_cleanup;
GRANT DELETE ON retrieval.index_jobs, retrieval.outbox TO retrieval_cleanup;
GRANT INSERT (event_id,event_type,aggregate_id,payload,occurred_at,next_attempt_at) ON retrieval.outbox TO retrieval_cleanup;
