CREATE TABLE retrieval.book_lifecycle (
    book_id text PRIMARY KEY REFERENCES retrieval.metadata_facts(book_id),
    lifecycle_version bigint NOT NULL CHECK (lifecycle_version >= 1),
    state text NOT NULL CHECK (state IN ('active','reindexing','deleting','deleted')),
    active_job_id text REFERENCES retrieval.index_jobs(id),
    event_id text NOT NULL UNIQUE,
    command_id text,
    event_type text NOT NULL,
    payload_digest bytea NOT NULL CHECK (octet_length(payload_digest) = 32),
    cleanup_pending boolean NOT NULL DEFAULT false,
    cleanup_attempts integer NOT NULL DEFAULT 0 CHECK (cleanup_attempts >= 0),
    cleanup_next_attempt_at timestamptz,
    correlation_id text NOT NULL,
    updated_at timestamptz NOT NULL
);

ALTER TABLE retrieval.index_jobs
    ADD COLUMN lifecycle_version bigint NOT NULL DEFAULT 1 CHECK (lifecycle_version >= 1),
    ADD COLUMN finalization_inflight boolean NOT NULL DEFAULT false,
    ADD COLUMN finalization_lease_expires_at timestamptz;

ALTER TABLE retrieval.metadata_facts
    ADD COLUMN media_type text NOT NULL DEFAULT 'application/pdf'
    CHECK (media_type IN ('application/pdf','application/epub+zip'));

ALTER TABLE retrieval.evidence
    ADD COLUMN media_type text NOT NULL DEFAULT 'application/pdf'
    CHECK (media_type IN ('application/pdf','application/epub+zip'));

ALTER TABLE retrieval.documents
    ADD COLUMN media_type text NOT NULL DEFAULT 'application/pdf'
    CHECK (media_type IN ('application/pdf','application/epub+zip'));

ALTER TABLE retrieval.index_jobs
    DROP CONSTRAINT IF EXISTS index_jobs_book_id_source_sha256_manifest_sha256_profile_digest_key;

CREATE INDEX retrieval_book_lifecycle_cleanup_idx
    ON retrieval.book_lifecycle(cleanup_next_attempt_at)
    WHERE cleanup_pending;

INSERT INTO retrieval.book_lifecycle(
    book_id,lifecycle_version,state,active_job_id,event_id,event_type,payload_digest,
    correlation_id,updated_at
)
SELECT m.book_id,1,'active',j.id,m.event_id || ':lifecycle-v1','legacy-v1',
       m.payload_digest,m.correlation_id,GREATEST(m.received_at,coalesce(j.updated_at,m.received_at))
FROM retrieval.metadata_facts m
LEFT JOIN LATERAL (
    SELECT id,updated_at
    FROM retrieval.index_jobs
    WHERE book_id=m.book_id AND state='indexed'
    ORDER BY updated_at DESC,id DESC
    LIMIT 1
) j ON true
ON CONFLICT (book_id) DO NOTHING;

GRANT SELECT ON retrieval.book_lifecycle TO retrieval_search;
GRANT SELECT, INSERT, UPDATE ON retrieval.book_lifecycle TO retrieval_planner;
GRANT SELECT, UPDATE ON retrieval.book_lifecycle TO retrieval_indexer, retrieval_cleanup;
