DELETE FROM retrieval.summary_assessment_cache;

ALTER TABLE retrieval.summary_assessment_cache
    ADD COLUMN IF NOT EXISTS topic_hash TEXT CHECK (topic_hash IS NULL OR topic_hash ~ '^[a-f0-9]{64}$'),
    ADD COLUMN IF NOT EXISTS guard_hash TEXT CHECK (guard_hash IS NULL OR guard_hash ~ '^[a-f0-9]{64}$'),
    ADD COLUMN IF NOT EXISTS last_accessed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    ADD COLUMN IF NOT EXISTS hit_count BIGINT NOT NULL DEFAULT 0 CHECK (hit_count >= 0);

ALTER TABLE retrieval.summary_assessment_cache
    ALTER COLUMN query_embedding DROP NOT NULL,
    DROP COLUMN topic_tokens,
    DROP COLUMN guard_tokens,
    DROP CONSTRAINT IF EXISTS summary_assessment_cache_query_embedding_check;

ALTER TABLE retrieval.summary_assessment_cache
    ADD CONSTRAINT summary_assessment_cache_query_embedding_v2_check
    CHECK (query_embedding IS NULL OR octet_length(query_embedding) > 0);

DROP INDEX IF EXISTS retrieval.retrieval_summary_assessment_cache_negative_idx;

CREATE INDEX IF NOT EXISTS retrieval_summary_assessment_cache_negative_v2_idx
    ON retrieval.summary_assessment_cache
        (provider_profile, passage_hash, topic_hash, guard_hash, last_accessed_at DESC)
    WHERE NOT relevant AND query_embedding IS NOT NULL;

CREATE INDEX IF NOT EXISTS retrieval_summary_assessment_cache_eviction_idx
    ON retrieval.summary_assessment_cache (hit_count DESC, last_accessed_at DESC, updated_at DESC);
