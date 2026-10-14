DROP TRIGGER IF EXISTS inspection_reports_require_evidence ON inspection_reports;
DROP FUNCTION IF EXISTS refuse_inspection_reports_without_evidence();
DROP TRIGGER IF EXISTS inspection_reports_append_only ON inspection_reports;
DROP FUNCTION IF EXISTS prevent_inspection_reports_mutation();
DROP INDEX IF EXISTS inspection_reports_session_idx;
DROP TABLE IF EXISTS inspection_reports;
