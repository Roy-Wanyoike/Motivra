-- Revert job_transitions immutability enforcement.

DROP TRIGGER IF EXISTS job_transitions_append_only ON job_transitions;
DROP FUNCTION IF EXISTS prevent_job_transitions_mutation();
