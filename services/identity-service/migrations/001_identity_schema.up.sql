CREATE SCHEMA IF NOT EXISTS identity AUTHORIZATION identity_migrator;

CREATE TABLE identity.users (
    id                TEXT        PRIMARY KEY,
    display_name      TEXT,
    email             TEXT,
    email_fingerprint BYTEA,
    password_hash     TEXT,
    role              TEXT        NOT NULL,
    status            TEXT        NOT NULL,
    email_verified_at TIMESTAMPTZ NOT NULL,
    reviewed_by       TEXT,
    reviewed_at       TIMESTAMPTZ,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT users_email_role_unique UNIQUE (email, role),
    CONSTRAINT users_email_canonical_check CHECK (email = lower(btrim(email))),
    CONSTRAINT users_email_fingerprint_check CHECK (
        email_fingerprint IS NULL OR octet_length(email_fingerprint) = 32
    ),
    CONSTRAINT users_role_check CHECK (role IN ('admin', 'librarian', 'reader')),
    CONSTRAINT users_status_check CHECK (status IN ('pending', 'active', 'rejected')),
    CONSTRAINT users_role_status_check CHECK (role = 'librarian' OR status = 'active'),
    CONSTRAINT users_review_check CHECK (
        (status = 'pending' AND role = 'librarian'
            AND reviewed_by IS NULL AND reviewed_at IS NULL
            AND display_name IS NOT NULL AND email IS NOT NULL
            AND email_fingerprint IS NOT NULL AND password_hash IS NOT NULL) OR
        (status = 'active' AND role = 'librarian'
            AND reviewed_by IS NOT NULL AND reviewed_at IS NOT NULL
            AND display_name IS NOT NULL AND email IS NOT NULL
            AND email_fingerprint IS NOT NULL AND password_hash IS NOT NULL) OR
        (status = 'rejected' AND role = 'librarian'
            AND reviewed_by IS NOT NULL AND reviewed_at IS NOT NULL
            AND ((display_name IS NOT NULL AND email IS NOT NULL
                    AND email_fingerprint IS NOT NULL AND password_hash IS NOT NULL) OR
                 (display_name IS NULL AND email IS NULL
                    AND email_fingerprint IS NULL AND password_hash IS NULL))) OR
        (status = 'active' AND role IN ('reader', 'admin')
            AND reviewed_by IS NULL AND reviewed_at IS NULL
            AND display_name IS NOT NULL AND email IS NOT NULL
            AND email_fingerprint IS NOT NULL AND password_hash IS NOT NULL)
    )
);

CREATE UNIQUE INDEX users_single_admin_idx
    ON identity.users ((role))
    WHERE role = 'admin';

CREATE UNIQUE INDEX users_email_fingerprint_role_idx
    ON identity.users (email_fingerprint, role)
    WHERE email_fingerprint IS NOT NULL;

CREATE INDEX users_pending_librarians_idx
    ON identity.users (created_at, id)
    WHERE role = 'librarian' AND status = 'pending';

CREATE TABLE identity.sessions (
    id           UUID        PRIMARY KEY,
    user_id      TEXT        NOT NULL REFERENCES identity.users(id) ON DELETE CASCADE,
    family_id    UUID        NOT NULL,
    expires_at   TIMESTAMPTZ NOT NULL,
    revoked_at   TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL,
    last_used_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX sessions_active_user_idx
    ON identity.sessions (user_id, expires_at)
    WHERE revoked_at IS NULL;

CREATE INDEX sessions_family_idx
    ON identity.sessions (family_id);

CREATE TABLE identity.refresh_tokens (
    id             UUID        PRIMARY KEY,
    session_id     UUID        NOT NULL REFERENCES identity.sessions(id) ON DELETE CASCADE,
    token_hash     BYTEA       NOT NULL UNIQUE,
    expires_at     TIMESTAMPTZ NOT NULL,
    consumed_at    TIMESTAMPTZ,
    replaced_by_id UUID        REFERENCES identity.refresh_tokens(id),
    created_at     TIMESTAMPTZ NOT NULL
);

CREATE INDEX refresh_tokens_active_hash_idx
    ON identity.refresh_tokens (token_hash)
    WHERE consumed_at IS NULL;

CREATE TABLE identity.registration_verifications (
    id                UUID        PRIMARY KEY,
    token_hash        BYTEA       NOT NULL UNIQUE CHECK (octet_length(token_hash) = 32),
    display_name      TEXT        NOT NULL,
    email             TEXT        NOT NULL,
    email_fingerprint BYTEA       NOT NULL CHECK (octet_length(email_fingerprint) = 32),
    password_hash     TEXT        NOT NULL,
    role              TEXT        NOT NULL CHECK (role IN ('reader', 'librarian')),
    expires_at        TIMESTAMPTZ NOT NULL,
    consumed_at       TIMESTAMPTZ,
    last_sent_at      TIMESTAMPTZ NOT NULL,
    created_at        TIMESTAMPTZ NOT NULL,

    CONSTRAINT registration_verifications_email_canonical_check
        CHECK (email = lower(btrim(email)))
);

CREATE UNIQUE INDEX registration_verifications_pending_email_idx
    ON identity.registration_verifications (email_fingerprint)
    WHERE consumed_at IS NULL;

CREATE INDEX registration_verifications_expiry_idx
    ON identity.registration_verifications (expires_at)
    WHERE consumed_at IS NULL;

CREATE TABLE identity.password_reset_challenges (
    email_fingerprint BYTEA       PRIMARY KEY,
    code_hash         BYTEA       NOT NULL,
    expires_at        TIMESTAMPTZ NOT NULL,
    attempts          INTEGER     NOT NULL DEFAULT 0 CHECK (attempts >= 0 AND attempts <= 5),
    last_sent_at      TIMESTAMPTZ NOT NULL,
    grant_hash        BYTEA,
    grant_expires_at  TIMESTAMPTZ,
    consumed_at       TIMESTAMPTZ,
    created_at        TIMESTAMPTZ NOT NULL
);

CREATE INDEX password_reset_challenges_expiry_idx
    ON identity.password_reset_challenges (expires_at);

CREATE TABLE identity.email_outbox (
    id              UUID        PRIMARY KEY,
    message_type    TEXT        NOT NULL CHECK (message_type IN ('verify_registration', 'password_reset_code')),
    key_id          TEXT        NOT NULL,
    nonce           BYTEA,
    ciphertext      BYTEA,
    attempts        INTEGER     NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at TIMESTAMPTZ NOT NULL,
    leased_until    TIMESTAMPTZ,
    delivered_at    TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL,

    CONSTRAINT email_outbox_payload_check CHECK (
        (delivered_at IS NULL AND nonce IS NOT NULL AND ciphertext IS NOT NULL) OR
        (delivered_at IS NOT NULL AND nonce IS NULL AND ciphertext IS NULL)
    )
);

CREATE INDEX email_outbox_delivery_idx
    ON identity.email_outbox (next_attempt_at, created_at)
    WHERE delivered_at IS NULL AND attempts < 10;

CREATE OR REPLACE FUNCTION identity.protect_user_review_fields()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, identity
AS $function$
BEGIN
    IF OLD.status = 'rejected' AND NEW.status = 'pending' THEN
        IF NEW.role <> 'librarian' OR
           NEW.id IS DISTINCT FROM OLD.id OR
           NEW.email IS DISTINCT FROM OLD.email OR
           NEW.email_fingerprint IS DISTINCT FROM OLD.email_fingerprint OR
           NEW.email_verified_at IS DISTINCT FROM OLD.email_verified_at OR
           NEW.reviewed_by IS NOT NULL OR NEW.reviewed_at IS NOT NULL OR
           (to_jsonb(NEW) - ARRAY['display_name', 'password_hash', 'created_at', 'status', 'reviewed_by', 'reviewed_at'])
             IS DISTINCT FROM
           (to_jsonb(OLD) - ARRAY['display_name', 'password_hash', 'created_at', 'status', 'reviewed_by', 'reviewed_at']) THEN
            RAISE EXCEPTION 'identity rejected librarian reapplication is invalid';
        END IF;
        RETURN NEW;
    END IF;

    IF OLD.reviewed_by IS NOT NULL AND
       (NEW.reviewed_by IS DISTINCT FROM OLD.reviewed_by OR
        NEW.reviewed_at IS DISTINCT FROM OLD.reviewed_at) THEN
        RAISE EXCEPTION 'identity review fields are immutable';
    END IF;

    IF OLD.status <> 'pending' AND NEW.status IS DISTINCT FROM OLD.status THEN
        RAISE EXCEPTION 'identity decision is final';
    END IF;

    RETURN NEW;
END
$function$;

CREATE TRIGGER protect_user_review_fields
BEFORE UPDATE ON identity.users
FOR EACH ROW
EXECUTE FUNCTION identity.protect_user_review_fields();

CREATE OR REPLACE FUNCTION identity.notify_pending_librarians_changed()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, identity
AS $function$
BEGIN
    IF (TG_OP = 'INSERT' AND NEW.role = 'librarian' AND NEW.status = 'pending') OR
       (TG_OP = 'UPDATE' AND NEW.role = 'librarian' AND NEW.status = 'pending' AND OLD.status <> 'pending') OR
       (TG_OP = 'UPDATE' AND OLD.role = 'librarian' AND OLD.status = 'pending' AND NEW.status <> 'pending') THEN
        PERFORM pg_notify('identity_pending_librarians_changed', '{"version":1}');
    END IF;

    RETURN NEW;
END
$function$;

CREATE TRIGGER notify_pending_librarians_changed
AFTER INSERT OR UPDATE OF status ON identity.users
FOR EACH ROW
EXECUTE FUNCTION identity.notify_pending_librarians_changed();

GRANT USAGE ON SCHEMA identity TO identity_runtime;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA identity TO identity_runtime;
