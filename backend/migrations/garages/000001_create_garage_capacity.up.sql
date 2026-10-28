-- Garages domain (issue #170): one partner garage's declared service
-- capacity — bays, operating hours, technician slots — so appointments
-- (issue #169) and dispatch scoring can respect it. The domain is new: this
-- is chain migration 000001 for backend/migrations/garages (ADR-0003
-- per-domain chains; the bounded context is documented as "Garages" in
-- ARCHITECTURE.md §1).
--
-- Tenancy: exactly one capacity row per tenant, keyed by the tenant_id the
-- authenticated GARAGE principal carries in its validated token (the UNIQUE
-- constraint is the "single row per tenant" requirement). The tenant
-- namespace is the identity domain's organizational registry; per ADR-0003
-- no cross-domain FK is declared — the column is a logical reference, the
-- same posture as jobs.vehicle_id.
--
-- DB-level constraints carry the integrity rules the API validates
-- server-side (defense in depth): non-negative counts and a sane operating
-- window (opening_time < closing_time).
CREATE TABLE garage_capacity (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id         uuid NOT NULL,
    bays              integer NOT NULL DEFAULT 0 CHECK (bays >= 0),
    opening_time      time NOT NULL DEFAULT '08:00',
    closing_time      time NOT NULL DEFAULT '18:00',
    technician_slots  integer NOT NULL DEFAULT 0 CHECK (technician_slots >= 0),
    updated_by        uuid NULL,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT garage_capacity_single_row_per_tenant UNIQUE (tenant_id),
    CONSTRAINT garage_capacity_operating_hours CHECK (opening_time < closing_time)
);
