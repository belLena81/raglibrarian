ALTER TABLE retrieval.index_jobs
    ADD COLUMN vector_cleanup_pending boolean NOT NULL DEFAULT false,
    ADD COLUMN vector_cleanup_attempts integer NOT NULL DEFAULT 0 CHECK (vector_cleanup_attempts >= 0),
    ADD COLUMN vector_cleanup_next_attempt_at timestamptz;

CREATE INDEX retrieval_index_jobs_vector_cleanup_idx
    ON retrieval.index_jobs(vector_cleanup_next_attempt_at)
    WHERE vector_cleanup_pending;

GRANT SELECT, UPDATE ON retrieval.index_jobs TO retrieval_cleanup;
