-- Normalize legacy values before enforcing the canonical email invariant.
-- Abort rather than choosing an account when two rows canonicalize identically.
DO $migration$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM identity.users
        GROUP BY lower(btrim(email))
        HAVING count(*) > 1
    ) THEN
        RAISE EXCEPTION 'identity email normalization has canonical collisions';
    END IF;
END
$migration$;

UPDATE identity.users
SET email = lower(btrim(email))
WHERE email <> lower(btrim(email));

DO $migration$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conname = 'users_email_canonical_check'
          AND conrelid = 'identity.users'::regclass
    ) THEN
        ALTER TABLE identity.users
            ADD CONSTRAINT users_email_canonical_check
            CHECK (email = lower(btrim(email)));
    END IF;
END
$migration$;
