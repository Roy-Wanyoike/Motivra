-- Identity domain: absolute session lifetime cap (hardening issue #81).
-- Rotation refreshes expires_at (idle cap, 30 days) but never extends
-- absolute_expires_at, so an actively-rotated session still ends at most
-- 90 days after its first login. Existing rows get the default from
-- migration time; development data only — production rolls from zero.

ALTER TABLE sessions
    ADD COLUMN absolute_expires_at timestamptz NOT NULL DEFAULT now() + interval '90 days';
