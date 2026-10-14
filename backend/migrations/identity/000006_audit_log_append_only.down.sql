-- Revert audit_log immutability enforcement.

DROP TRIGGER IF EXISTS audit_log_append_only ON audit_log;
DROP FUNCTION IF EXISTS prevent_audit_log_mutation();
