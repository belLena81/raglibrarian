-- Migration: 003_create_books
-- Stores the library catalogue. Each book moves through an ingestion pipeline
-- tracked by index_status.

CREATE TABLE IF NOT EXISTS books (
                                     id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    title        TEXT        NOT NULL,
    author       TEXT        NOT NULL,
    year         INT         NOT NULL,
    index_status TEXT        NOT NULL DEFAULT 'pending',
    tags         TEXT[]      NOT NULL DEFAULT '{}',
    s3_key       TEXT,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT books_title_author_year_unique
    UNIQUE (title, author, year),
    CONSTRAINT books_index_status_check
    CHECK (index_status IN ('pending', 'indexing', 'indexed', 'failed'))
    );

CREATE INDEX IF NOT EXISTS books_index_status_idx ON books (index_status);
CREATE INDEX IF NOT EXISTS books_author_idx        ON books (author);
CREATE INDEX IF NOT EXISTS books_year_idx          ON books (year);
CREATE INDEX IF NOT EXISTS books_tags_gin_idx      ON books USING GIN (tags);