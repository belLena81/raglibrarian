CREATE TABLE IF NOT EXISTS retrieval.summary_assessment_cache (
    provider_profile TEXT        NOT NULL,
    question_hash    TEXT        NOT NULL CHECK (question_hash ~ '^[a-f0-9]{64}$'),
    passage_hash     TEXT        NOT NULL CHECK (passage_hash ~ '^[a-f0-9]{64}$'),
    topic_tokens     TEXT[]      NOT NULL,
    guard_tokens     TEXT[]      NOT NULL,
    query_embedding  BYTEA       NOT NULL CHECK (octet_length(query_embedding) > 0),
    relevant         BOOLEAN     NOT NULL,
    summary          TEXT        NOT NULL,
    expires_at       TIMESTAMPTZ NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (provider_profile, question_hash, passage_hash)
);

CREATE INDEX IF NOT EXISTS retrieval_summary_assessment_cache_negative_idx
    ON retrieval.summary_assessment_cache (provider_profile, passage_hash, expires_at)
    WHERE NOT relevant;

CREATE INDEX IF NOT EXISTS retrieval_summary_assessment_cache_expiry_idx
    ON retrieval.summary_assessment_cache (expires_at);

GRANT SELECT, INSERT, UPDATE, DELETE ON retrieval.summary_assessment_cache TO retrieval_search;
