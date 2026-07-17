DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM identity.users GROUP BY email HAVING count(*) > 1
    ) THEN
        RAISE EXCEPTION 'cannot restore email-only uniqueness while an email has multiple roles';
    END IF;
END $$;

DROP INDEX identity.users_email_fingerprint_role_idx;

ALTER TABLE identity.users
    DROP CONSTRAINT users_email_role_unique;

ALTER TABLE identity.users
    ADD CONSTRAINT users_email_unique UNIQUE (email);

CREATE UNIQUE INDEX users_email_fingerprint_idx
    ON identity.users (email_fingerprint)
    WHERE email_fingerprint IS NOT NULL;
