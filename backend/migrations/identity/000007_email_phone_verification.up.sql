-- Identity domain: email/phone verification flow (issue #86, ADR-0004).
-- Each contact channel carries its own verification timestamp; a NULL
-- timestamp means the channel is unverified. Existing rows (development
-- data only — production rolls from zero) therefore start unverified and
-- become subject to the verification grace gate, exactly like fresh
-- registrations.

ALTER TABLE users
    ADD COLUMN email_verified_at timestamptz,
    ADD COLUMN phone_verified_at timestamptz;

-- Single-use verification tokens. Only the keyed HMAC-SHA256 digest of the
-- opaque client token is stored; consumed_at implements single use.
CREATE TABLE verification_tokens (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    channel     text        NOT NULL CHECK (channel IN ('email', 'phone')),
    token_hash  text        NOT NULL UNIQUE,
    expires_at  timestamptz NOT NULL,
    consumed_at timestamptz,
    created_at  timestamptz NOT NULL DEFAULT now()
);

-- Resend cooldown/limit queries are per user+channel.
CREATE INDEX verification_tokens_user_channel_idx ON verification_tokens (user_id, channel);
