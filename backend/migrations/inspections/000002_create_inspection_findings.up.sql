-- Findings are the recorded per-item results of one inspection session.
-- Append-only by design: a corrected item result is a NEW row (the newest
-- record per item wins at report assembly). Two triggers enforce the
-- integrity contract at the database level:
--   1. append-only — UPDATE/DELETE raise (mirrors vehicle_service_history);
--   2. sealed after submit — INSERT is refused unless the parent session is
--      IN_PROGRESS, so a submitted session's findings are physically
--      immutable (issue #61 acceptance criterion).
CREATE TABLE inspection_findings (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id  uuid NOT NULL REFERENCES inspection_sessions(id) ON DELETE CASCADE,
    item_key    text NOT NULL,
    result      text NOT NULL CHECK (result IN ('PASS','FAIL','ATTENTION','NOT_APPLICABLE')),
    severity    text NOT NULL CHECK (severity IN ('GREEN','AMBER','RED')),
    notes       text NOT NULL DEFAULT '',
    recorded_by uuid NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX inspection_findings_session_idx ON inspection_findings (session_id, created_at);

CREATE FUNCTION prevent_inspection_findings_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'inspection_findings is append-only; corrections are new records';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER inspection_findings_append_only
    BEFORE UPDATE OR DELETE ON inspection_findings
    FOR EACH ROW EXECUTE FUNCTION prevent_inspection_findings_mutation();

CREATE FUNCTION refuse_inspection_findings_outside_active_session() RETURNS trigger AS $$
BEGIN
    IF (SELECT status FROM inspection_sessions WHERE id = NEW.session_id) <> 'IN_PROGRESS' THEN
        RAISE EXCEPTION 'inspection_findings is sealed: session % is not IN_PROGRESS', NEW.session_id;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER inspection_findings_sealed_after_submit
    BEFORE INSERT ON inspection_findings
    FOR EACH ROW EXECUTE FUNCTION refuse_inspection_findings_outside_active_session();
