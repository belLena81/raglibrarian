-- Migration: 001_create_users
-- Creates the users table for authentication.
--
-- Design decisions:
--   - id is a TEXT UUID rather than BIGSERIAL. UUIDs are safe to expose in
--     tokens and URLs without leaking row counts. The cost is ~4 bytes extra
--     storage per row — negligible at our scale.
--   - email has a unique constraint enforced at the DB level, not just the
--     application level, so concurrent registrations cannot create duplicates
--     even under race conditions.
--   - password_hash stores the full bcrypt output (60 chars max for $2b$ format).
--     VARCHAR(72) is sufficient; we use TEXT for future-proofing if we ever
--     switch algorithms.
--   - role is a TEXT with a CHECK constraint rather than a Postgres ENUM.
--     ENUMs are painful to alter in Postgres (requires a schema migration for
--     every new value). A CHECK constraint is trivially extended.
--   - created_at is TIMESTAMPTZ (with timezone). Storing UTC everywhere is
--     correct, but TIMESTAMPTZ survives DST changes and server timezone
--     reconfigurations safely.

CREATE TABLE IF NOT EXISTS users (
    id            TEXT        NOT NULL PRIMARY KEY,
    email         TEXT        NOT NULL,
    password_hash TEXT        NOT NULL,
    role          TEXT        NOT NULL DEFAULT 'reader',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT users_email_unique UNIQUE (email),
    CONSTRAINT users_role_check   CHECK  (role IN ('admin', 'reader'))
);

CREATE INDEX IF NOT EXISTS users_email_idx ON users (email);