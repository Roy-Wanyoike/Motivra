-- Identity domain: refresh sessions (ADR-0004).
-- Refresh tokens are stored only as SHA-256 hashes; rotation revokes the
-- previous row by setting revoked_at.
CREATE TABLE sessions (
    id                 uuid        PRIMARY KEY,
    user_id            uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    refresh_token_hash text        NOT NULL UNIQUE,
    device_name        text        NOT NULL DEFAULT '',
    ip_address         text        NOT NULL DEFAULT '',
    created_at         timestamptz NOT NULL DEFAULT now(),
    expires_at         timestamptz NOT NULL,
    revoked_at         timestamptz
);

CREATE INDEX sessions_user_id_idx ON sessions (user_id);
