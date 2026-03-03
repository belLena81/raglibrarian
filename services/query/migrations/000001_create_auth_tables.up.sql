-- migrations/000001_create_auth_tables.up.sql
-- Auth schema for raglibrarian Query Service.
-- Role constraint is DB-level belt-and-suspenders alongside domain validation.

CREATE TABLE IF NOT EXISTS users (
    id            UUID        PRIMARY KEY,
    name          TEXT        NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
    email         TEXT        NOT NULL,
    password_hash TEXT        NOT NULL,
    role          TEXT        NOT NULL CHECK (role IN ('reader', 'librarian', 'admin')),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Case-insensitive unique email
CREATE UNIQUE INDEX IF NOT EXISTS users_email_key ON users (LOWER(email));

CREATE TABLE IF NOT EXISTS refresh_tokens (
    id         UUID        PRIMARY KEY,
    user_id    UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash TEXT        NOT NULL UNIQUE,  -- SHA-256 hex, never plaintext
    expires_at TIMESTAMPTZ NOT NULL,
    revoked_at TIMESTAMPTZ,                  -- NULL = active
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Fast lookup by hash on every /auth/refresh call
CREATE INDEX IF NOT EXISTS refresh_tokens_hash_idx   ON refresh_tokens (token_hash);
-- Fast revocation sweep on logout
CREATE INDEX IF NOT EXISTS refresh_tokens_user_id_idx ON refresh_tokens (user_id);
