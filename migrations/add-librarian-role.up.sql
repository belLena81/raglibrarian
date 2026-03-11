-- Migration: 002_add_librarian_role
-- Extends the users_role_check constraint to accept the new 'librarian' role.
--
-- Design decisions:
--   - We cannot ALTER a CHECK constraint in-place in Postgres; we must DROP
--     the old constraint and ADD a new one in the same transaction. This is
--     safe because the operation does not touch any row data.
--   - Wrapped in a transaction so a partial failure leaves the schema
--     unchanged — the old constraint remains intact.
--   - Existing 'admin' and 'reader' rows are unaffected; the new constraint
--     is a strict superset of the old one.

BEGIN;

ALTER TABLE users
DROP CONSTRAINT users_role_check;

ALTER TABLE users
    ADD CONSTRAINT users_role_check
        CHECK (role IN ('admin', 'librarian', 'reader'));

COMMIT;