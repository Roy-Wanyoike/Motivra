#!/usr/bin/env bash
#
# seed_demo.sh — idempotent demo dataset for Motivra (issues #25 + #31).
#
# Creates, in ONE transaction, against the REAL migration schema
# (backend/migrations/{identity,vehicles,jobs}):
#   - 1 demo tenant (tenant_id UUID used by user_roles/vehicles — note:
#     there is no `tenants` table yet; tenant ids are plain UUID columns)
#   - 1 demo garage represented by its GARAGE-role admin user (the garages
#     domain has no migration chain yet — backend/migrations/garages/ is a
#     stub; this is the honest, schema-respecting stand-in)
#   - 3 technicians (users + TECHNICIAN role grants)
#   - 1 customer (vehicle owner) + 2 registered vehicles
#   - 4 vehicle-service-history entries (the "passport history")
#   - 1 service request converted into 1 job in ASSIGNED state, with its
#     append-only transition trail (CREATED→TRIAGING→DISPATCHING→ASSIGNED)
#     and 1 job assignment
#
# Idempotency: fixed UUIDs + ON CONFLICT DO NOTHING / WHERE NOT EXISTS
# guards — safe to re-run; re-runs create nothing new and never modify
# existing rows, so the append-only trigger on vehicle_service_history
# (backend/migrations/vehicles/000003) is never provoked and history rows
# are never UPDATEd or DELETEd.
#
# Seeded login: all demo users share password "MotivraDemo!2026"
# (argon2id PHC hashes pre-generated with the exact backend parameters:
# m=65536,t=3,p=4 — verified against backend/identity/password.go
# VerifyPassword). This is a PUBLIC demo credential, not a secret.
#
# Usage:
#   scripts/seed_demo.sh [CONNECTION_URL]
#   MOTIVRA_DATABASE_URL=... scripts/seed_demo.sh
#
# Resolution order: $1 > MOTIVRA_DATABASE_URL > default (app stack mapping:
# postgres 5433 on localhost — `make stack-up` exposes it there; for the
# dev deps stack pass postgres://motivra:motivra@localhost:5432/motivra?sslmode=disable).

set -euo pipefail

CONN="${1:-${MOTIVRA_DATABASE_URL:-postgres://motivra:motivra@localhost:5433/motivra?sslmode=disable}}"

command -v psql >/dev/null 2>&1 || {
  echo "error: psql not found on PATH (install postgresql-client)" >&2
  exit 1
}

# --- Fixed identifiers (deterministic across runs) --------------------------
DEMO_TENANT_ID="a0000000-0000-4000-8000-000000000001"
CUSTOMER_ID="a0000000-0000-4000-8000-000000000002"
TECH_1_ID="a0000000-0000-4000-8000-000000000003"
TECH_2_ID="a0000000-0000-4000-8000-000000000004"
TECH_3_ID="a0000000-0000-4000-8000-000000000005"
GARAGE_ADMIN_ID="a0000000-0000-4000-8000-000000000006"
VEHICLE_1_ID="b0000000-0000-4000-8000-000000000001"
VEHICLE_2_ID="b0000000-0000-4000-8000-000000000002"
HIST_1_ID="c0000000-0000-4000-8000-000000000001"  # vehicle 1: service
HIST_2_ID="c0000000-0000-4000-8000-000000000002"  # vehicle 1: inspection
HIST_3_ID="c0000000-0000-4000-8000-000000000003"  # vehicle 1: mileage
HIST_4_ID="c0000000-0000-4000-8000-000000000004"  # vehicle 2: repair
REQUEST_ID="d0000000-0000-4000-8000-000000000001"
JOB_ID="e0000000-0000-4000-8000-000000000001"
ASSIGNMENT_ID="f0000000-0000-4000-8000-000000000001"

# --- Argon2id PHC hashes of the demo password (per-user random salts) -------
HASH_CUSTOMER='$argon2id$v=19$m=65536,t=3,p=4$rqn91I/oJ5NSIVIJ3RcRUw$ajym2PH+zQK7BMaQsZQ342dcLwPZLVfVZ20CwuzhgEQ'
HASH_TECH_1='$argon2id$v=19$m=65536,t=3,p=4$HQ19aVbJyo6JcmLTAab1ng$IBn9RMHipbnCFEsFelMJKOamW/goTWZz4qco5+fvkCY'
HASH_TECH_2='$argon2id$v=19$m=65536,t=3,p=4$vG/upMTLWeoKs83YApBGvg$aB6/46XsszS9gEL+HVPFf6RXHDSFzg5JTHN1rASc40k'
HASH_TECH_3='$argon2id$v=19$m=65536,t=3,p=4$d9cgiNb7YvyXZGIEMcEkMA$oAK6ixwssVW9UMwveCHDQeJz/hDHAEIay0FzOWcfUG4'
HASH_GARAGE='$argon2id$v=19$m=65536,t=3,p=4$h9cuwW89Ptsp7dpDh7faeg$Nyk9adLi+w3X8CEAnRzFM83XSfRmCm9ICLxMt97/WVA'

echo "Seeding demo data -> ${CONN%%\?*} (credentials redacted)"
echo "  transaction: single (all-or-nothing)"

if psql "$CONN" \
  -v ON_ERROR_STOP=1 \
  --single-transaction \
  --set=demo_tenant="$DEMO_TENANT_ID" \
  --set=pw_customer="$HASH_CUSTOMER" \
  --set=pw_tech1="$HASH_TECH_1" \
  --set=pw_tech2="$HASH_TECH_2" \
  --set=pw_tech3="$HASH_TECH_3" \
  --set=pw_garage="$HASH_GARAGE" \
  <<'SQL'

-- ===========================================================================
-- Identity: demo tenant users (schema: backend/migrations/identity/000001-000002)
-- Fixed UUIDs + ON CONFLICT DO NOTHING => re-runs are no-ops.
-- ===========================================================================

INSERT INTO users (id, email, phone, password_hash, full_name, status) VALUES
  ('a0000000-0000-4000-8000-000000000002', 'customer@demo.motivra.app', '+254700000002', :'pw_customer', 'Wanjiku Demo', 'active'),
  ('a0000000-0000-4000-8000-000000000003', 'tech1@demo.motivra.app',  '+254700000003', :'pw_tech1',    'Otieno Tech One', 'active'),
  ('a0000000-0000-4000-8000-000000000004', 'tech2@demo.motivra.app',  '+254700000004', :'pw_tech2',    'Kamau Tech Two', 'active'),
  ('a0000000-0000-4000-8000-000000000005', 'tech3@demo.motivra.app',  '+254700000005', :'pw_tech3',    'Njeri Tech Three', 'active'),
  ('a0000000-0000-4000-8000-000000000006', 'garage@demo.motivra.app', '+254700000006', :'pw_garage',   'Mwangi Garage Admin', 'active')
ON CONFLICT DO NOTHING;

-- Role grants (ADR-0004 fixed role set). Customer: personal grant
-- (tenant_id NULL). Technicians + garage admin: scoped to the demo tenant.
INSERT INTO user_roles (user_id, role, tenant_id) VALUES
  ('a0000000-0000-4000-8000-000000000002', 'CUSTOMER',   NULL),
  ('a0000000-0000-4000-8000-000000000003', 'TECHNICIAN', :'demo_tenant'::uuid),
  ('a0000000-0000-4000-8000-000000000004', 'TECHNICIAN', :'demo_tenant'::uuid),
  ('a0000000-0000-4000-8000-000000000005', 'TECHNICIAN', :'demo_tenant'::uuid),
  ('a0000000-0000-4000-8000-000000000006', 'GARAGE',     :'demo_tenant'::uuid)
ON CONFLICT DO NOTHING;

-- ===========================================================================
-- Vehicles: registry + passport history
-- (schema: backend/migrations/vehicles/000001-000003)
-- vehicle_service_history is APPEND-ONLY (DB trigger). This script only
-- ever INSERTs history rows; it never UPDATEs or DELETEs.
-- ===========================================================================

INSERT INTO vehicles (id, tenant_id, owner_user_id, vin_normalized, make, model, year_of_manufacture, plate, color, mileage_latest_km) VALUES
  ('b0000000-0000-4000-8000-000000000001', :'demo_tenant'::uuid, 'a0000000-0000-4000-8000-000000000002', 'JTMHV05J404123456', 'Toyota',  'Hilux',  2019, 'KDA 123A', 'White', 128400),
  ('b0000000-0000-4000-8000-000000000002', :'demo_tenant'::uuid, 'a0000000-0000-4000-8000-000000000002', 'KMHD84LF5GU123456', 'Hyundai', 'i20',    2021, 'KCX 456B', 'Silver', 52100)
ON CONFLICT DO NOTHING;

INSERT INTO vehicle_service_history (id, vehicle_id, event_type, occurred_at, odometer_km, summary, evidence_ref, recorded_by) VALUES
  ('c0000000-0000-4000-8000-000000000001', 'b0000000-0000-4000-8000-000000000001', 'service',    '2025-11-14T09:00:00+03:00', 120000, '60,000 km major service: oil, filters, brake fluid.', 's3://motivra-demo/evidence/hilux-60k-service.pdf', 'a0000000-0000-4000-8000-000000000006'),
  ('c0000000-0000-4000-8000-000000000002', 'b0000000-0000-4000-8000-000000000001', 'inspection', '2026-01-20T14:30:00+03:00', 124800, 'Pre-trip inspection: tyres 6mm, brakes 60%, no leaks.', '', 'a0000000-0000-4000-8000-000000000006'),
  ('c0000000-0000-4000-8000-000000000003', 'b0000000-0000-4000-8000-000000000001', 'mileage',    '2026-02-10T08:15:00+03:00', 128400, 'Odometer reading recorded at depot check-in.', '', 'a0000000-0000-4000-8000-000000000002'),
  ('c0000000-0000-4000-8000-000000000004', 'b0000000-0000-4000-8000-000000000002', 'repair',     '2026-02-01T11:00:00+03:00', 50950,  'Replaced front-left wheel bearing after noise report.', '', 'a0000000-0000-4000-8000-000000000006')
ON CONFLICT (id) DO NOTHING;

-- ===========================================================================
-- Jobs: intake -> converted request -> ASSIGNED job
-- (schema: backend/migrations/jobs/000001-000004)
-- ===========================================================================

INSERT INTO service_requests (id, customer_id, vehicle_id, description, location_lat, location_lng, address_text, status) VALUES
  ('d0000000-0000-4000-8000-000000000001', 'a0000000-0000-4000-8000-000000000002', 'b0000000-0000-4000-8000-000000000001',
   'Brakes squealing under light braking; request inspection and pad replacement estimate.',
   -1.2921, 36.8219, 'Ngong Road, Nairobi', 'converted')
ON CONFLICT (id) DO NOTHING;

INSERT INTO jobs (id, service_request_id, customer_id, vehicle_id, status, problem_summary, technician_id) VALUES
  ('e0000000-0000-4000-8000-000000000001', 'd0000000-0000-4000-8000-000000000001', 'a0000000-0000-4000-8000-000000000002', 'b0000000-0000-4000-8000-000000000001',
   'ASSIGNED', 'Brake squeal on light braking; suspected worn pads.', 'a0000000-0000-4000-8000-000000000003')
ON CONFLICT (id) DO NOTHING;

-- Append-only transition trail (readers query by job_id + created_at).
-- No natural key exists, so each row is guarded by NOT EXISTS to stay
-- idempotent without ever updating history.
INSERT INTO job_transitions (job_id, from_status, to_status, actor_id, reason)
SELECT 'e0000000-0000-4000-8000-000000000001', 'CREATED', 'TRIAGING', 'a0000000-0000-4000-8000-000000000006', 'Intake triaged from demo service request'
WHERE NOT EXISTS (SELECT 1 FROM job_transitions WHERE job_id = 'e0000000-0000-4000-8000-000000000001' AND to_status = 'TRIAGING');

INSERT INTO job_transitions (job_id, from_status, to_status, actor_id, reason)
SELECT 'e0000000-0000-4000-8000-000000000001', 'TRIAGING', 'DISPATCHING', 'a0000000-0000-4000-8000-000000000006', 'Ready for technician matching'
WHERE NOT EXISTS (SELECT 1 FROM job_transitions WHERE job_id = 'e0000000-0000-4000-8000-000000000001' AND to_status = 'DISPATCHING');

INSERT INTO job_transitions (job_id, from_status, to_status, actor_id, reason)
SELECT 'e0000000-0000-4000-8000-000000000001', 'DISPATCHING', 'ASSIGNED', 'a0000000-0000-4000-8000-000000000006', 'Assigned to Otieno Tech One (deterministic scoring)'
WHERE NOT EXISTS (SELECT 1 FROM job_transitions WHERE job_id = 'e0000000-0000-4000-8000-000000000001' AND to_status = 'ASSIGNED');

-- One assignment row per dispatch attempt; reassignment appends new rows.
INSERT INTO job_assignments (id, job_id, technician_id, assigned_by, accepted_at, status)
SELECT 'f0000000-0000-4000-8000-000000000001', 'e0000000-0000-4000-8000-000000000001', 'a0000000-0000-4000-8000-000000000003', 'a0000000-0000-4000-8000-000000000006', NULL, 'assigned'
WHERE NOT EXISTS (SELECT 1 FROM job_assignments WHERE job_id = 'e0000000-0000-4000-8000-000000000001' AND technician_id = 'a0000000-0000-4000-8000-000000000003' AND status = 'assigned');

-- ===========================================================================
-- Created-summary
-- ===========================================================================

WITH demo_users(user_id) AS (VALUES
  ('a0000000-0000-4000-8000-000000000002'::uuid),
  ('a0000000-0000-4000-8000-000000000003'::uuid),
  ('a0000000-0000-4000-8000-000000000004'::uuid),
  ('a0000000-0000-4000-8000-000000000005'::uuid),
  ('a0000000-0000-4000-8000-000000000006'::uuid)
)
SELECT 'demo tenant id'          AS item, :'demo_tenant'::text AS value
UNION ALL SELECT 'users present',            count(*)::text FROM users     WHERE id IN (SELECT user_id FROM demo_users)
UNION ALL SELECT 'role grants present',      count(*)::text FROM user_roles WHERE user_id IN (SELECT user_id FROM demo_users)
UNION ALL SELECT 'vehicles present',         count(*)::text FROM vehicles  WHERE id IN ('b0000000-0000-4000-8000-000000000001','b0000000-0000-4000-8000-000000000002')
UNION ALL SELECT 'history entries present',  count(*)::text FROM vehicle_service_history WHERE vehicle_id IN ('b0000000-0000-4000-8000-000000000001','b0000000-0000-4000-8000-000000000002')
UNION ALL SELECT 'service requests present', count(*)::text FROM service_requests WHERE id = 'd0000000-0000-4000-8000-000000000001'
UNION ALL SELECT 'jobs present',             count(*)::text FROM jobs      WHERE id = 'e0000000-0000-4000-8000-000000000001'
UNION ALL SELECT 'transitions present',      count(*)::text FROM job_transitions WHERE job_id = 'e0000000-0000-4000-8000-000000000001'
UNION ALL SELECT 'assignments present',      count(*)::text FROM job_assignments WHERE job_id = 'e0000000-0000-4000-8000-000000000001';

SQL
then
  echo "Seed OK (single transaction committed)."
  echo "Demo login: customer@demo.motivra.app | tech1@demo.motivra.app | garage@demo.motivra.app  (password: MotivraDemo!2026)"
  echo "Re-running this script is safe: existing rows are never modified or deleted."
else
  status=$?
  echo "Seed FAILED (psql exit $status) — transaction rolled back, nothing was created." >&2
  exit "$status"
fi
