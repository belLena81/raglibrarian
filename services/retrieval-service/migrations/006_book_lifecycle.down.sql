REVOKE SELECT, UPDATE ON retrieval.book_lifecycle FROM retrieval_cleanup;
REVOKE SELECT, UPDATE ON retrieval.book_lifecycle FROM retrieval_indexer;
REVOKE SELECT, INSERT, UPDATE ON retrieval.book_lifecycle FROM retrieval_planner;
REVOKE SELECT ON retrieval.book_lifecycle FROM retrieval_search;

DROP INDEX IF EXISTS retrieval.retrieval_book_lifecycle_cleanup_idx;
DROP TABLE IF EXISTS retrieval.book_lifecycle;

ALTER TABLE retrieval.index_jobs
    DROP COLUMN IF EXISTS finalization_lease_expires_at,
    DROP COLUMN IF EXISTS finalization_inflight,
    DROP COLUMN IF EXISTS lifecycle_version;
ALTER TABLE retrieval.metadata_facts
    DROP COLUMN IF EXISTS media_type;
ALTER TABLE retrieval.evidence
    DROP COLUMN IF EXISTS media_type;
ALTER TABLE retrieval.documents
    DROP COLUMN IF EXISTS media_type;
ALTER TABLE retrieval.index_jobs
    ADD CONSTRAINT index_jobs_book_id_source_sha256_manifest_sha256_profile_digest_key
    UNIQUE (book_id,source_sha256,manifest_sha256,profile_digest);
