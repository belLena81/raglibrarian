REVOKE DELETE ON retrieval.index_jobs, retrieval.outbox FROM retrieval_indexer;

REVOKE INSERT (event_id,event_type,aggregate_id,payload,occurred_at,next_attempt_at) ON retrieval.outbox FROM retrieval_cleanup;
REVOKE DELETE ON retrieval.index_jobs, retrieval.outbox FROM retrieval_cleanup;
REVOKE SELECT (book_id), DELETE ON retrieval.manifest_facts FROM retrieval_cleanup;
REVOKE SELECT (book_id), UPDATE (title,author,publication_year,tags) ON retrieval.metadata_facts FROM retrieval_cleanup;

REVOKE DELETE ON retrieval.manifest_facts, retrieval.index_jobs, retrieval.outbox FROM retrieval_planner;

REVOKE SELECT, INSERT, UPDATE ON retrieval.book_lifecycle FROM retrieval_runtime;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM retrieval.book_lifecycle l
        WHERE NOT EXISTS (
            SELECT 1
            FROM retrieval.metadata_facts m
            WHERE m.book_id=l.book_id
        )
    ) THEN
        RAISE EXCEPTION 'cannot roll back retrieval lifecycle remediation while orphan lifecycle fences exist';
    END IF;
END
$$;

ALTER TABLE retrieval.book_lifecycle
    ADD CONSTRAINT book_lifecycle_book_id_fkey
    FOREIGN KEY (book_id) REFERENCES retrieval.metadata_facts(book_id);
