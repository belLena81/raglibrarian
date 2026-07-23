ALTER TABLE retrieval.index_jobs
    DROP CONSTRAINT IF EXISTS index_jobs_book_id_source_sha256_manifest_sha256_profile_di_key;

ALTER TABLE retrieval.book_lifecycle
    DROP CONSTRAINT IF EXISTS book_lifecycle_book_id_fkey;

GRANT SELECT, INSERT, UPDATE ON retrieval.book_lifecycle TO retrieval_runtime;
REVOKE DELETE ON retrieval.book_lifecycle FROM retrieval_runtime;

GRANT DELETE ON retrieval.manifest_facts, retrieval.index_jobs TO retrieval_planner;

GRANT SELECT (book_id), UPDATE (title,author,publication_year,tags) ON retrieval.metadata_facts TO retrieval_cleanup;
GRANT SELECT (book_id), DELETE ON retrieval.manifest_facts TO retrieval_cleanup;
GRANT DELETE ON retrieval.index_jobs TO retrieval_cleanup;
GRANT INSERT (event_id,event_type,aggregate_id,payload,occurred_at,next_attempt_at) ON retrieval.outbox TO retrieval_cleanup;

GRANT DELETE ON retrieval.index_jobs, retrieval.outbox TO retrieval_indexer;
