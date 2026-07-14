CREATE TABLE identity.sessions (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES identity.users(id) ON DELETE CASCADE,
    family_id UUID NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    revoked_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL,
    last_used_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX sessions_active_user_idx ON identity.sessions (user_id, expires_at) WHERE revoked_at IS NULL;
CREATE INDEX sessions_family_idx ON identity.sessions (family_id);

CREATE TABLE identity.refresh_tokens (
    id UUID PRIMARY KEY,
    session_id UUID NOT NULL REFERENCES identity.sessions(id) ON DELETE CASCADE,
    token_hash BYTEA NOT NULL UNIQUE,
    expires_at TIMESTAMPTZ NOT NULL,
    consumed_at TIMESTAMPTZ,
    replaced_by_id UUID REFERENCES identity.refresh_tokens(id),
    created_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX refresh_tokens_active_hash_idx ON identity.refresh_tokens (token_hash) WHERE consumed_at IS NULL;
