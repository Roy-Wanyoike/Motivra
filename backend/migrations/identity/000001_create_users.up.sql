-- Identity domain: user accounts (ADR-0003 data ownership, ADR-0004 security baseline).
-- Email uniqueness is enforced case-insensitively via a LOWER() index instead of
-- the citext extension, keeping the identity chain dependency-free.
CREATE TABLE users (
    id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    email         text        NOT NULL,
    phone         text,
    password_hash text        NOT NULL,
    full_name     text        NOT NULL,
    status        text        NOT NULL DEFAULT 'active'
                  CHECK (status IN ('active', 'suspended', 'deleted')),
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX users_email_key ON users (LOWER(email));

-- Partial index: empty phone values are stored as NULL so that several users
-- may omit a phone number without colliding.
CREATE UNIQUE INDEX users_phone_key ON users (phone) WHERE phone IS NOT NULL;
