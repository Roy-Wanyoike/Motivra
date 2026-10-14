DROP TRIGGER IF EXISTS inspection_evidence_sealed_after_submit ON inspection_evidence;
DROP FUNCTION IF EXISTS refuse_inspection_evidence_outside_active_session();
DROP TRIGGER IF EXISTS inspection_evidence_append_only ON inspection_evidence;
DROP FUNCTION IF EXISTS prevent_inspection_evidence_mutation();
DROP INDEX IF EXISTS inspection_evidence_session_idx;
DROP TABLE IF EXISTS inspection_evidence;
