-- Revert the email/phone verification flow (issue #86).

DROP TABLE IF EXISTS verification_tokens;

ALTER TABLE users
    DROP COLUMN IF EXISTS email_verified_at,
    DROP COLUMN IF EXISTS phone_verified_at;
