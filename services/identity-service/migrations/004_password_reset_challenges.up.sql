CREATE TABLE identity.password_reset_challenges (
    email_fingerprint bytea PRIMARY KEY,
    code_hash bytea NOT NULL,
    expires_at timestamptz NOT NULL,
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0 AND attempts <= 5),
    last_sent_at timestamptz NOT NULL,
    grant_hash bytea,
    grant_expires_at timestamptz,
    consumed_at timestamptz,
    created_at timestamptz NOT NULL
);

CREATE INDEX password_reset_challenges_expiry_idx ON identity.password_reset_challenges (expires_at);
