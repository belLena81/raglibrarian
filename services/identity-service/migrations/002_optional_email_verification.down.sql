DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM identity.users WHERE email_verified_at IS NULL) THEN
        RAISE EXCEPTION 'cannot restore required email verification while unverified users exist';
    END IF;
END $$;

ALTER TABLE identity.users
    ALTER COLUMN email_verified_at SET NOT NULL;
