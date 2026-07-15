-- Canonicalization discards presentation-only casing and surrounding spaces;
-- rollback removes the invariant but intentionally preserves valid data.
ALTER TABLE identity.users
    DROP CONSTRAINT IF EXISTS users_email_canonical_check;
