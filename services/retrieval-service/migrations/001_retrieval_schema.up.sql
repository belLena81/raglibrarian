CREATE SCHEMA IF NOT EXISTS retrieval;

CREATE TABLE retrieval.metadata_facts (
    book_id text PRIMARY KEY,
    event_id text NOT NULL UNIQUE,
    payload_digest bytea NOT NULL CHECK (octet_length(payload_digest) = 32),
    source_sha256 bytea NOT NULL CHECK (octet_length(source_sha256) = 32),
    title text NOT NULL,
    author text NOT NULL,
    publication_year integer NOT NULL CHECK (publication_year >= 0),
    tags text[] NOT NULL DEFAULT '{}',
    correlation_id text NOT NULL,
    causation_id text NOT NULL,
    occurred_at timestamptz NOT NULL,
    received_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE retrieval.manifest_facts (
    book_id text PRIMARY KEY,
    event_id text NOT NULL UNIQUE,
    payload_digest bytea NOT NULL CHECK (octet_length(payload_digest) = 32),
    source_sha256 bytea NOT NULL CHECK (octet_length(source_sha256) = 32),
    manifest_sha256 bytea NOT NULL CHECK (octet_length(manifest_sha256) = 32),
    manifest_reference text NOT NULL,
    manifest_payload bytea NOT NULL,
    correlation_id text NOT NULL,
    causation_id text NOT NULL,
    occurred_at timestamptz NOT NULL,
    received_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE retrieval.index_jobs (
    id text PRIMARY KEY,
    book_id text NOT NULL REFERENCES retrieval.metadata_facts(book_id),
    source_sha256 bytea NOT NULL CHECK (octet_length(source_sha256) = 32),
    manifest_sha256 bytea NOT NULL CHECK (octet_length(manifest_sha256) = 32),
    profile_digest bytea NOT NULL CHECK (octet_length(profile_digest) = 32),
    state text NOT NULL CHECK (state IN ('pending','indexed','failed')),
    expected_batches integer NOT NULL CHECK (expected_batches > 0),
    evidence_count integer NOT NULL DEFAULT 0 CHECK (evidence_count >= 0),
    failure_category text,
    correlation_id text NOT NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    UNIQUE (book_id, source_sha256, manifest_sha256, profile_digest)
);

CREATE TABLE retrieval.index_batches (
    id text PRIMARY KEY,
    job_id text NOT NULL REFERENCES retrieval.index_jobs(id) ON DELETE CASCADE,
    shard_reference text NOT NULL,
    shard_sha256 bytea NOT NULL CHECK (octet_length(shard_sha256) = 32),
    compressed_byte_size bigint NOT NULL CHECK (compressed_byte_size > 0),
    uncompressed_byte_size bigint NOT NULL CHECK (uncompressed_byte_size > 0),
    chunk_count integer NOT NULL CHECK (chunk_count > 0),
    state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending','processing','completed','failed')),
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    lease_owner text,
    lease_expires_at timestamptz,
    next_attempt_at timestamptz,
    updated_at timestamptz NOT NULL,
    UNIQUE (job_id, shard_reference)
);

CREATE TABLE retrieval.evidence (
    evidence_id text PRIMARY KEY,
    chunk_id text NOT NULL,
    job_id text NOT NULL REFERENCES retrieval.index_jobs(id) ON DELETE CASCADE,
    book_id text NOT NULL,
    title text NOT NULL,
    author text NOT NULL,
    publication_year integer NOT NULL,
    tags text[] NOT NULL,
    chapter text NOT NULL DEFAULT '',
    section text NOT NULL DEFAULT '',
    page_start integer NOT NULL CHECK (page_start >= 0),
    page_end integer NOT NULL CHECK (page_end >= page_start),
    passage text NOT NULL,
    content_sha256 bytea NOT NULL CHECK (octet_length(content_sha256) = 32),
    created_at timestamptz NOT NULL,
    UNIQUE (job_id, chunk_id)
);

CREATE TABLE retrieval.outbox (
    event_id text PRIMARY KEY,
    event_type text NOT NULL,
    aggregate_id text NOT NULL,
    payload bytea NOT NULL,
    occurred_at timestamptz NOT NULL,
    published_at timestamptz,
    attempts integer NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL
);
CREATE INDEX retrieval_outbox_pending_idx ON retrieval.outbox(next_attempt_at) WHERE published_at IS NULL;

REVOKE ALL ON SCHEMA retrieval FROM PUBLIC;
REVOKE ALL ON ALL TABLES IN SCHEMA retrieval FROM PUBLIC;
GRANT USAGE ON SCHEMA retrieval TO retrieval_runtime, retrieval_search, retrieval_planner, retrieval_indexer, retrieval_dispatcher, retrieval_cleanup, retrieval_e2e;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA retrieval TO retrieval_runtime;
GRANT SELECT ON retrieval.index_jobs TO retrieval_search;
GRANT SELECT ON ALL TABLES IN SCHEMA retrieval TO retrieval_e2e;
GRANT SELECT, INSERT, UPDATE ON retrieval.metadata_facts, retrieval.manifest_facts, retrieval.index_jobs, retrieval.index_batches, retrieval.outbox TO retrieval_planner;
GRANT SELECT, INSERT, UPDATE ON retrieval.index_jobs, retrieval.index_batches, retrieval.evidence, retrieval.outbox TO retrieval_indexer;
GRANT SELECT ON retrieval.metadata_facts TO retrieval_indexer;
GRANT SELECT, UPDATE ON retrieval.outbox TO retrieval_dispatcher;
GRANT SELECT, UPDATE ON retrieval.index_batches, retrieval.outbox TO retrieval_cleanup;
