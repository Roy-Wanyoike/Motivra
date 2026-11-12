-- Identity domain: technician skill-verification status (issue #166,
-- graduate-mechanic pipeline #126 Phase 2).
--
-- The verification lifecycle is a server-authoritative attribute of the
-- technician identity profile:
--
--     unverified | pending | verified | rejected
--
-- users.verification_status carries the CURRENT status (NULL = never
-- assessed, which every reader renders as "unverified"); it is written
-- ONLY by the verification API inside the same transaction that appends
-- the decision record below and its audit_log entry, so the attribute can
-- never diverge from its append-only trail. Dispatch (issue #167) treats
-- any status other than exactly "verified" as ineligible — fail-closed —
-- so both "pending" and "rejected" are safe lifecycle extensions.
--
-- technician_verifications is the append-only decision record: one row per
-- status change (approve/reject), never updated, never deleted; a
-- correction is a NEW superseding row. Credential evidence is referenced,
-- never stored: assessment template/session ids point into the Inspections
-- context by id only (no cross-domain FKs, ADR-0003), and evidence_refs
-- are opaque evidence handles — signed URLs and raw documents never land
-- here (docs/PII-HANDLING.md).

ALTER TABLE users
    ADD COLUMN verification_status text
    CONSTRAINT users_verification_status_check
        CHECK (verification_status IN ('unverified', 'pending', 'verified', 'rejected'));

CREATE TABLE technician_verifications (
    id                     uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    technician_id          uuid        NOT NULL REFERENCES users (id),
    status                 text        NOT NULL CHECK (status IN ('unverified', 'pending', 'verified', 'rejected')),
    reason                 text        NOT NULL DEFAULT '',
    actor_id               uuid        NOT NULL,
    assessment_template_id uuid,
    assessment_session_id  uuid,
    evidence_refs          text[]      NOT NULL DEFAULT '{}',
    created_at             timestamptz NOT NULL DEFAULT now()
);

-- The status read model resolves the technician's newest decision.
CREATE INDEX technician_verifications_technician_idx
    ON technician_verifications (technician_id, created_at DESC);

-- Write-once enforcement at the database level (mirrors audit_log,
-- issue #83): rows are appended, never mutated.
CREATE FUNCTION prevent_technician_verifications_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'technician_verifications is append-only';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER technician_verifications_append_only
    BEFORE UPDATE OR DELETE ON technician_verifications
    FOR EACH ROW EXECUTE FUNCTION prevent_technician_verifications_mutation();
