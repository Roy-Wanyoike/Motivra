-- Issued inspection reports: the verifiable, append-only record assembled
-- from the session's findings and evidence hashes. One report per session
-- version (UNIQUE); the canonical content_hash is computed by the service
-- (see backend/inspections/report.go for the exact algorithm). Two triggers
-- enforce integrity at the database level:
--   1. append-only — UPDATE/DELETE raise;
--   2. evidence precondition — INSERT is refused for a session without at
--      least one evidence record, so a report can never exist without
--      evidence hashes (issue #61 acceptance criterion).
CREATE TABLE inspection_reports (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id       uuid NOT NULL REFERENCES inspection_sessions(id) ON DELETE CASCADE,
    version          integer NOT NULL CHECK (version > 0),
    vehicle_id       uuid NULL,
    job_id           uuid NULL,
    inspector_id     uuid NOT NULL,
    template_id      uuid NOT NULL,
    template_name    text NOT NULL,
    template_version integer NOT NULL,
    findings         jsonb NOT NULL,
    evidence         jsonb NOT NULL,
    findings_count   integer NOT NULL CHECK (findings_count >= 0),
    evidence_count   integer NOT NULL CHECK (evidence_count >= 0),
    content_hash     text NOT NULL CHECK (content_hash ~ '^[0-9a-f]{64}$'),
    issued_by        uuid NULL,
    notes            text NOT NULL DEFAULT '',
    created_at       timestamptz NOT NULL DEFAULT now(),
    UNIQUE (session_id, version)
);

CREATE INDEX inspection_reports_session_idx ON inspection_reports (session_id, version DESC);

CREATE FUNCTION prevent_inspection_reports_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'inspection_reports is append-only';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER inspection_reports_append_only
    BEFORE UPDATE OR DELETE ON inspection_reports
    FOR EACH ROW EXECUTE FUNCTION prevent_inspection_reports_mutation();

CREATE FUNCTION refuse_inspection_reports_without_evidence() RETURNS trigger AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM inspection_evidence WHERE session_id = NEW.session_id
    ) THEN
        RAISE EXCEPTION 'inspection_reports requires evidence: session % carries no evidence hashes', NEW.session_id;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER inspection_reports_require_evidence
    BEFORE INSERT ON inspection_reports
    FOR EACH ROW EXECUTE FUNCTION refuse_inspection_reports_without_evidence();
