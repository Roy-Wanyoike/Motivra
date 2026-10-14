-- Identity domain: enforce the audit trail's write-once guarantee at the
-- database level (ADR-0004 "audit records are write-once", integrity issue
-- #83). Until now the guarantee was convention; any UPDATE or DELETE — bug
-- or compromise — would have silently succeeded.

CREATE FUNCTION prevent_audit_log_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'audit_log is append-only';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER audit_log_append_only
    BEFORE UPDATE OR DELETE ON audit_log
    FOR EACH ROW EXECUTE FUNCTION prevent_audit_log_mutation();
