-- Inspections domain: template-driven inspection sessions. The session is
-- the aggregate root; findings, evidence, reports and the transition audit
-- all belong to exactly one session. vehicle_class resolves the template at
-- creation (snapshot columns freeze the resolution against later registry
-- changes); tenant_id is the owning organization when the creator acted
-- under a tenant claim. vehicle_id / job_id / customer_id are cross-domain
-- references held by identifier only — no cross-domain FKs (ADR-0003).
CREATE TABLE inspection_sessions (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id        uuid NULL,
    vehicle_id       uuid NULL,
    job_id           uuid NULL,
    customer_id      uuid NULL,
    vehicle_class    text NOT NULL CHECK (vehicle_class IN ('MOTORCYCLE','SEDAN','SUV','PICKUP','TRUCK','BUS')),
    template_id      uuid NOT NULL,
    template_name    text NOT NULL,
    template_version integer NOT NULL CHECK (template_version > 0),
    inspector_id     uuid NOT NULL,
    status           text NOT NULL CHECK (status IN ('CREATED','IN_PROGRESS','SUBMITTED','REPORT_AVAILABLE')),
    started_at       timestamptz NULL,
    submitted_at     timestamptz NULL,
    reported_at      timestamptz NULL,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now()
);

-- The scope predicate filters on tenant_id / inspector_id / customer_id;
-- every read path in the store uses one of these shapes.
CREATE INDEX inspection_sessions_tenant_idx ON inspection_sessions (tenant_id);
CREATE INDEX inspection_sessions_inspector_idx ON inspection_sessions (inspector_id);
CREATE INDEX inspection_sessions_customer_idx ON inspection_sessions (customer_id);
CREATE INDEX inspection_sessions_vehicle_idx ON inspection_sessions (vehicle_id);
CREATE INDEX inspection_sessions_job_idx ON inspection_sessions (job_id);
