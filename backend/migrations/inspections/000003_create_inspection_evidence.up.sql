-- Evidence provenance records: one row certifies that a piece of media
-- (photo, reading, document) existed at captured_at under captured_by, via
-- its content hash. No media storage lives here (out of scope, issue #61);
-- records reference hashes only. Append-only and sealed after submit with
-- the same trigger discipline as findings.
CREATE TABLE inspection_evidence (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id   uuid NOT NULL REFERENCES inspection_sessions(id) ON DELETE CASCADE,
    finding_id   uuid NULL REFERENCES inspection_findings(id) ON DELETE CASCADE,
    kind         text NOT NULL CHECK (kind IN ('PHOTO','READING','DOCUMENT')),
    content_hash text NOT NULL CHECK (content_hash ~ '^[0-9a-f]{64}$'),
    captured_at  timestamptz NOT NULL,
    captured_by  uuid NULL,
    uploaded     boolean NOT NULL DEFAULT false,
    notes        text NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX inspection_evidence_session_idx ON inspection_evidence (session_id, created_at);

CREATE FUNCTION prevent_inspection_evidence_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'inspection_evidence is append-only; corrections are new records';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER inspection_evidence_append_only
    BEFORE UPDATE OR DELETE ON inspection_evidence
    FOR EACH ROW EXECUTE FUNCTION prevent_inspection_evidence_mutation();

CREATE FUNCTION refuse_inspection_evidence_outside_active_session() RETURNS trigger AS $$
BEGIN
    IF (SELECT status FROM inspection_sessions WHERE id = NEW.session_id) <> 'IN_PROGRESS' THEN
        RAISE EXCEPTION 'inspection_evidence is sealed: session % is not IN_PROGRESS', NEW.session_id;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER inspection_evidence_sealed_after_submit
    BEFORE INSERT ON inspection_evidence
    FOR EACH ROW EXECUTE FUNCTION refuse_inspection_evidence_outside_active_session();
