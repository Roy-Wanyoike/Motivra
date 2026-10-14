-- Jobs tenancy (issue #88, ADR-0004 "tenant_id scoping is mandatory on every
-- multi-tenant table"): all four jobs tables gain tenant_id. The column is
-- NULLABLE by the same posture as vehicles/inspections/payments: a NULL
-- tenant is a personal (tenant-less) principal's row, which keeps the
-- resource-level scope (customer ownership / technician assignment) — the
-- documented fail-closed tenancy model until a tenant registry exists.
--
-- Backfill: rows created before this migration carry no derivable tenant.
-- The principal→tenant mapping lives in identity.user_roles.tenant_id
-- (ADR-0003 data ownership: the jobs chain does not join identity tables),
-- and every grant in existence today is personal (tenant_id NULL), so there
-- is nothing to derive. Pre-existing rows therefore keep tenant_id NULL and
-- remain visible exactly as before (customer/technician/operator); NEW rows
-- are stamped with the actor's tenant claim when the token carries one, and
-- child rows (assignments, transitions) denormalize the parent job's
-- tenant_id inside the same transaction. Read scopes derive isolation from
-- tenant_id where it is set and fail closed otherwise (jobs.ReadScope).

ALTER TABLE service_requests ADD COLUMN tenant_id uuid NULL;
ALTER TABLE jobs            ADD COLUMN tenant_id uuid NULL;
ALTER TABLE job_assignments ADD COLUMN tenant_id uuid NULL;
ALTER TABLE job_transitions ADD COLUMN tenant_id uuid NULL;

-- In-domain backfill: child rows inherit the parent job's tenant (the only
-- derivable mapping); the parent job/request tenants themselves have no
-- derivable source (see above) and stay NULL.
UPDATE job_assignments a
SET    tenant_id = j.tenant_id
FROM   jobs j
WHERE  a.job_id = j.id;

UPDATE job_transitions t
SET    tenant_id = j.tenant_id
FROM   jobs j
WHERE  t.job_id = j.id;

-- Tenant-filtered read support, mirroring the inspections chain.
CREATE INDEX service_requests_tenant_idx ON service_requests (tenant_id);
CREATE INDEX jobs_tenant_idx             ON jobs (tenant_id);
CREATE INDEX job_assignments_tenant_idx  ON job_assignments (tenant_id);
CREATE INDEX job_transitions_tenant_idx  ON job_transitions (tenant_id);
