LOCK TABLE retrieval.book_lifecycle,
           retrieval.index_jobs,
           retrieval.index_batches,
           retrieval.outbox
    IN ACCESS EXCLUSIVE MODE;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM retrieval.book_lifecycle
        WHERE state IN ('reindexing','deleting')
    ) OR EXISTS (
        SELECT 1
        FROM retrieval.index_jobs
        WHERE finalization_inflight
    ) OR EXISTS (
        SELECT 1
        FROM retrieval.index_batches
        WHERE state='processing'
    ) THEN
        RAISE EXCEPTION 'cannot roll back retrieval book lifecycle while lifecycle work is in flight';
    END IF;
END
$$;

CREATE TEMPORARY TABLE retrieval_m006_loser_jobs
ON COMMIT DROP
AS
WITH ranked_jobs AS (
    SELECT j.id,
           row_number() OVER (
               PARTITION BY j.book_id,j.source_sha256,j.manifest_sha256,j.profile_digest
               ORDER BY coalesce(l.active_job_id=j.id,false) DESC,
                        (j.state='indexed') DESC,
                        j.lifecycle_version DESC,
                        j.updated_at DESC,
                        j.id DESC
           ) AS generation_rank
    FROM retrieval.index_jobs j
    LEFT JOIN retrieval.book_lifecycle l ON l.book_id=j.book_id
)
SELECT id
FROM ranked_jobs
WHERE generation_rank > 1;

DELETE FROM retrieval.outbox o
USING retrieval_m006_loser_jobs loser
WHERE o.aggregate_id=loser.id
  AND o.published_at IS NULL;

DELETE FROM retrieval.index_jobs j
USING retrieval_m006_loser_jobs loser
WHERE j.id=loser.id;

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
