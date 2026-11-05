-- Human report-verification records (issue #185): the append-only trail of
-- human review decisions on issued inspection reports. Every VERIFIED /
-- REJECTED outcome a reviewer records is one immutable row; corrections and
-- reversals are NEW records (newest per report wins), mirroring the
-- findings discipline. The machine integrity recomputation
-- (GET .../report/verification) stays separate — this table holds the
-- human verdict plus the integrity snapshot captured at decision time.
--
-- (report_id, session_id) carries a composite FK onto the report's
-- UNIQUE (id, session_id) pair, so a row can never pair a report with a
-- session it does not belong to — even from raw SQL.
CREATE TABLE inspection_report_verifications (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    report_id          uuid NOT NULL REFERENCES inspection_reports(id) ON DELETE CASCADE,
    session_id         uuid NOT NULL REFERENCES inspection_sessions(id) ON DELETE CASCADE,
    report_version     integer NOT NULL CHECK (report_version > 0),
    outcome            text NOT NULL CHECK (outcome IN ('VERIFIED','REJECTED')),
    reviewer_id        uuid NOT NULL,
    note               text NOT NULL DEFAULT '' CHECK (char_length(note) <= 2000),
    integrity_verified boolean NOT NULL,
    created_at         timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX inspection_report_verifications_session_idx
    ON inspection_report_verifications (session_id, created_at);

CREATE INDEX inspection_report_verifications_report_idx
    ON inspection_report_verifications (report_id, created_at);

-- Pairing integrity: inspection_reports gains the explicit (id, session_id)
-- unique constraint the composite FK below references (id alone is already
-- the primary key, so this is purely additive).
ALTER TABLE inspection_reports
    ADD CONSTRAINT inspection_reports_id_session_key UNIQUE (id, session_id);

ALTER TABLE inspection_report_verifications
    ADD CONSTRAINT inspection_report_verifications_report_pair_fk
    FOREIGN KEY (report_id, session_id)
    REFERENCES inspection_reports (id, session_id) ON DELETE CASCADE;

-- Append-only: rows are never updated or deleted. A correction is a new
-- record; the newest record per report wins.
CREATE FUNCTION prevent_inspection_report_verifications_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'inspection_report_verifications is append-only';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER inspection_report_verifications_append_only
    BEFORE UPDATE OR DELETE ON inspection_report_verifications
    FOR EACH ROW EXECUTE FUNCTION prevent_inspection_report_verifications_mutation();

-- Status guard (defense in depth for the service rule): a human verdict can
-- only be recorded against a REPORT_AVAILABLE session — there is nothing to
-- verify before the report is issued.
CREATE FUNCTION refuse_inspection_report_verifications_outside_report_available() RETURNS trigger AS $$
BEGIN
    IF (SELECT status FROM inspection_sessions WHERE id = NEW.session_id) <> 'REPORT_AVAILABLE' THEN
        RAISE EXCEPTION 'inspection_report_verifications requires REPORT_AVAILABLE: session % is not reviewable', NEW.session_id;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER inspection_report_verifications_require_report_available
    BEFORE INSERT ON inspection_report_verifications
    FOR EACH ROW EXECUTE FUNCTION refuse_inspection_report_verifications_outside_report_available();
