-- Rollback of issue #166 technician verification: drop the append-only
-- decision records first (trigger + function), then the denormalized
-- current-status column on users.

DROP TRIGGER IF EXISTS technician_verifications_append_only ON technician_verifications;
DROP FUNCTION IF EXISTS prevent_technician_verifications_mutation();
DROP TABLE IF EXISTS technician_verifications;

ALTER TABLE users DROP CONSTRAINT IF EXISTS users_verification_status_check;
ALTER TABLE users DROP COLUMN IF EXISTS verification_status;
