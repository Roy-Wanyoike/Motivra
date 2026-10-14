-- Reverse of 000006_jobs_tenancy.up.sql: drop the tenant indexes and
-- columns. The denormalized child columns carry no information not
-- recoverable from the parent job, so the drop loses nothing.
DROP INDEX IF EXISTS job_transitions_tenant_idx;
DROP INDEX IF EXISTS job_assignments_tenant_idx;
DROP INDEX IF EXISTS jobs_tenant_idx;
DROP INDEX IF EXISTS service_requests_tenant_idx;

ALTER TABLE job_transitions DROP COLUMN IF EXISTS tenant_id;
ALTER TABLE job_assignments DROP COLUMN IF EXISTS tenant_id;
ALTER TABLE jobs            DROP COLUMN IF EXISTS tenant_id;
ALTER TABLE service_requests DROP COLUMN IF EXISTS tenant_id;
