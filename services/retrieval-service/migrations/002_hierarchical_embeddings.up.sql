CREATE TABLE retrieval.document_embedding_accumulators (
    job_id text PRIMARY KEY REFERENCES retrieval.index_jobs(id) ON DELETE CASCADE,
    vector_sum real[] NOT NULL CHECK (array_length(vector_sum, 1) = 768),
    chunk_count integer NOT NULL CHECK (chunk_count > 0),
    updated_at timestamptz NOT NULL
);

CREATE TABLE retrieval.documents (
    document_id text PRIMARY KEY,
    job_id text NOT NULL UNIQUE REFERENCES retrieval.index_jobs(id) ON DELETE CASCADE,
    book_id text NOT NULL,
    title text NOT NULL,
    author text NOT NULL,
    publication_year integer NOT NULL,
    tags text[] NOT NULL,
    chunk_count integer NOT NULL CHECK (chunk_count > 0),
    page_start integer NOT NULL CHECK (page_start >= 0),
    page_end integer NOT NULL CHECK (page_end >= page_start),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);

GRANT SELECT, INSERT, UPDATE ON retrieval.document_embedding_accumulators, retrieval.documents TO retrieval_runtime;
GRANT SELECT ON retrieval.documents TO retrieval_search;
GRANT SELECT ON retrieval.documents TO retrieval_e2e;
GRANT SELECT, INSERT, UPDATE ON retrieval.document_embedding_accumulators, retrieval.documents TO retrieval_indexer;
