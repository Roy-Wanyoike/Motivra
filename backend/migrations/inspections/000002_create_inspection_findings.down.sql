DROP TRIGGER IF EXISTS inspection_findings_sealed_after_submit ON inspection_findings;
DROP FUNCTION IF EXISTS refuse_inspection_findings_outside_active_session();
DROP TRIGGER IF EXISTS inspection_findings_append_only ON inspection_findings;
DROP FUNCTION IF EXISTS prevent_inspection_findings_mutation();
DROP INDEX IF EXISTS inspection_findings_session_idx;
DROP TABLE IF EXISTS inspection_findings;
