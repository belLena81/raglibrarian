ALTER TABLE identity.users
    DROP CONSTRAINT users_email_unique;

DROP INDEX identity.users_email_fingerprint_idx;

ALTER TABLE identity.users
    ADD CONSTRAINT users_email_role_unique UNIQUE (email, role);

CREATE UNIQUE INDEX users_email_fingerprint_role_idx
    ON identity.users (email_fingerprint, role)
    WHERE email_fingerprint IS NOT NULL;
