-- Garages domain (issue #169): the appointment calendar tied to service
-- requests. One appointment row is a calendar slot a garage holds for a
-- service request/job — referenced BY ID only (no cross-domain FK, the
-- same posture as jobs.vehicle_id; ADR-0003): the jobs domain is never
-- written from here.
--
-- Tenancy: tenant_id is the owning garage organization (the GARAGE token's
-- tenant claim, or the target garage a CUSTOMER addresses in the URL). It
-- is a logical reference into the identity domain's organizational
-- registry, never a client-free-for-all: every write derives it from the
-- authenticated principal or the addressed route, and every read is
-- tenant-scoped.
--
-- Lifecycle: status starts at 'requested' and moves through explicit
-- transitions only (requested → confirmed → completed, cancelled from
-- requested/confirmed, no_show from confirmed; terminal states immutable).
-- The CHECK constraint pins the vocabulary; the service layer owns the
-- transition graph and appends the audit_log row in the SAME transaction
-- as each transition (ADR-0004 audit seam).
--
-- Double-booking: the EXCLUDE constraint makes two ACTIVE (requested or
-- confirmed) appointments for the same tenant + bay whose one-hour slots
-- overlap impossible at the database level; the service layer performs the
-- same check transactionally (advisory lock + SELECT ... FOR UPDATE) for a
-- friendly 409, with the constraint as the concurrency backstop. A missing
-- bay is normalized to '' (the garage's general pool) so the constraint
-- covers unbayed bookings too. btree_gist is required for the text
-- equality strategy class; extensions are cluster-shared, so the down
-- migration deliberately leaves it installed.
CREATE EXTENSION IF NOT EXISTS btree_gist;

CREATE TABLE garage_appointments (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     uuid NOT NULL,
    job_id        uuid NOT NULL,
    scheduled_for timestamptz NOT NULL,
    bay           text NULL CHECK (bay IS NULL OR length(bay) <= 64),
    notes         text NULL CHECK (notes IS NULL OR length(notes) <= 1024),
    status        text NOT NULL DEFAULT 'requested'
                  CHECK (status IN ('requested', 'confirmed', 'completed', 'cancelled', 'no_show')),
    created_by    uuid NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT garage_appointments_active_slot_no_overlap
        EXCLUDE USING gist (
            tenant_id WITH =,
            COALESCE(bay, '') WITH =,
            tstzrange(scheduled_for, scheduled_for + interval '1 hour', '[)') WITH &&
        ) WHERE (status IN ('requested', 'confirmed'))
);

CREATE INDEX garage_appointments_tenant_schedule_idx
    ON garage_appointments (tenant_id, scheduled_for DESC);

CREATE INDEX garage_appointments_job_idx
    ON garage_appointments (job_id);
