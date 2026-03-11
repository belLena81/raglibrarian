-- Migration: 002_add_librarian_role (rollback)
-- Reverts the role CHECK constraint to the Iteration 2 definition.
--
-- WARNING: rolling this back while 'librarian' rows exist will leave those
-- rows violating the restored constraint. Ensure all librarian users are
-- re-assigned before running this down migration.

BEGIN;

ALTER TABLE users
DROP CONSTRAINT users_role_check;

ALTER TABLE users
    ADD CONSTRAINT users_role_check
        CHECK (role IN ('admin', 'reader'));

COMMIT;