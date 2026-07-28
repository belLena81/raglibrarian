CREATE INDEX IF NOT EXISTS retrieval_evidence_lexical_search_idx
    ON retrieval.evidence
    USING GIN (
        to_tsvector('simple', title || ' ' || author || ' ' || chapter || ' ' || section || ' ' || passage)
    );

GRANT SELECT ON retrieval.evidence TO retrieval_search;
