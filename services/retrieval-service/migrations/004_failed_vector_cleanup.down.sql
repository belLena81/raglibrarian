REVOKE SELECT, UPDATE ON retrieval.index_jobs FROM retrieval_cleanup;

DROP INDEX IF EXISTS retrieval.retrieval_index_jobs_vector_cleanup_idx;

ALTER TABLE retrieval.index_jobs
    DROP COLUMN IF EXISTS vector_cleanup_next_attempt_at,
    DROP COLUMN IF EXISTS vector_cleanup_attempts,
    DROP COLUMN IF EXISTS vector_cleanup_pending;
