-- Revert the absolute session lifetime cap.

ALTER TABLE sessions
    DROP COLUMN IF EXISTS absolute_expires_at;
