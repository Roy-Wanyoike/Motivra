#!/usr/bin/env bash
#
# purge_demo_data.sh — marker-scoped removal of ALL demo/seed rows from a
# Motivra database (issue #118). Companion to scripts/seed_demo.sh: where
# the seed inserts a closed, fixed set of demo rows, this script deletes
# exactly that set — and nothing else, by construction.
#
# What it deletes (demo markers only):
#   - users with the fixed demo UUIDs (a0000000-...-0002..0006) or any
#     *@demo.motivra.app email, plus their roles, sessions, verification
#     tokens and audit rows
#   - the demo tenant rows (tenant_id a0000000-...-0001)
#   - the demo vehicles (b0000000-...-0001/0002), their components and
#     passport-history entries
#   - the demo service request (d...0001), job (e...0001), transitions and
#     assignment (f...0001)
#   - any payments/inspections rows scoped to the demo users, job, vehicles
#     or tenant (the seed predates those domains, so normally zero)
# Rows WITHOUT demo markers are provably untouched: the script captures
# per-table (total, demo, preserved) counts before and after inside the same
# transaction and ROLLS BACK unless preserved counts are identical and every
# demo count lands at zero.
#
# Append-only tables (audit_log, job_transitions, vehicle_service_history,
# payment_intent_transitions, inspection_session_transitions) are deleted
# through the single sanctioned pattern (scripts/purge_expired.sql): a
# guarded window that disables the specific append-only trigger, deletes
# marker-scoped rows, and re-enables the trigger — all inside one
# transaction, so a failure rolls the trigger state back automatically.
#
# Usage:
#   DEMO_PURGE_CONFIRM=1 scripts/purge_demo_data.sh [CONNECTION_URL]
#   DEMO_PURGE_CONFIRM=1 BACKUP=0 scripts/purge_demo_data.sh ...   # skip backup (loud)
#
# URL resolution order: $1 > MOTIVRA_DATABASE_URL > app-stack default
# (127.0.0.1:5433) — identical to scripts/backup.sh and seed_demo.sh.
#
# Safety gates:
#   1. DEMO_PURGE_CONFIRM=1 must be set (exit 3 otherwise).
#   2. A fresh backup is taken first (BACKUP=0 to skip, discouraged).
#   3. Single transaction: any failure rolls back EVERYTHING, including
#      trigger disable/enable.
#   4. Preserved-count invariant asserted in-transaction (see above).
#
# After a successful run, run `make integrity-check DB_URL=...` (must be
# all-clear) and record the before/after table in the operations log.
#
# Unlike seed_demo.sh, this script does NOT refuse MOTIVRA_ENV=production:
# removing demo rows from a production database is exactly its purpose when
# a demo seed has landed there. The confirm gate + backup + marker scoping
# are the safety envelope.

set -euo pipefail

if [[ "${DEMO_PURGE_CONFIRM:-0}" != "1" ]]; then
  echo "refusing to purge: set DEMO_PURGE_CONFIRM=1 to acknowledge this" >&2
  echo "permanently deletes all demo-marker rows in the target database" >&2
  exit 3
fi

CONN="${1:-${MOTIVRA_DATABASE_URL:-postgres://motivra:motivra@127.0.0.1:5433/motivra?sslmode=disable}}"

command -v psql >/dev/null 2>&1 || { echo "psql not found on PATH" >&2; exit 4; }

echo "Purging demo data -> ${CONN%%\?*} (credentials redacted)"

if [[ "${BACKUP:-1}" == "1" ]]; then
  SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
  echo "Backup first (custom format, ./backups) ..."
  bash "${SCRIPT_DIR}/backup.sh" "$CONN" ./backups
else
  echo "!! BACKUP=0 — skipping the pre-purge backup (not recommended) !!"
fi

echo "  transaction: single (all-or-nothing, triggers re-armed on commit)"

psql "$CONN" \
  -v ON_ERROR_STOP=1 \
  --single-transaction \
<<'SQL'

-- ===========================================================================
-- Demo marker constants (mirrors scripts/seed_demo.sh — single source of
-- truth for the demo dataset's fixed identifiers).
-- ===========================================================================
CREATE TEMP TABLE _markers AS
SELECT
  ARRAY[
    'a0000000-0000-4000-8000-000000000002'::uuid,  -- customer
    'a0000000-0000-4000-8000-000000000003'::uuid,  -- tech1
    'a0000000-0000-4000-8000-000000000004'::uuid,  -- tech2
    'a0000000-0000-4000-8000-000000000005'::uuid,  -- tech3
    'a0000000-0000-4000-8000-000000000006'::uuid   -- garage admin
  ] AS user_ids,
  'a0000000-0000-4000-8000-000000000001'::uuid AS tenant_id,
  ARRAY[
    'b0000000-0000-4000-8000-000000000001'::uuid,  -- vehicle 1
    'b0000000-0000-4000-8000-000000000002'::uuid   -- vehicle 2
  ] AS vehicle_ids,
  'd0000000-0000-4000-8000-000000000001'::uuid AS request_id,
  'e0000000-0000-4000-8000-000000000001'::uuid AS job_id,
  'f0000000-0000-4000-8000-000000000001'::uuid AS assignment_id,
  '%@demo.motivra.app' AS email_pattern;

-- Resolve the ACTUAL demo users present (fixed ids OR the email marker) so
-- child-table scoping stays correct even if a seed variant used new ids.
CREATE TEMP TABLE _demo_users AS
SELECT u.id FROM users u, _markers m
WHERE u.id = ANY (m.user_ids) OR u.email LIKE m.email_pattern;

CREATE TEMP TABLE _demo_intents AS
SELECT p.id FROM payment_intents p, _markers m
WHERE p.tenant_id = m.tenant_id
   OR p.customer_id = ANY (SELECT id FROM _demo_users)
   OR p.created_by  = ANY (SELECT id FROM _demo_users)
   OR p.job_id = m.job_id
   OR p.vehicle_id = ANY (m.vehicle_ids);

CREATE TEMP TABLE _demo_isessions AS
SELECT i.id FROM inspection_sessions i, _markers m
WHERE i.tenant_id = m.tenant_id
   OR i.vehicle_id = ANY (m.vehicle_ids)
   OR i.job_id = m.job_id
   OR i.customer_id = ANY (SELECT id FROM _demo_users)
   OR i.inspector_id = ANY (SELECT id FROM _demo_users);

-- ===========================================================================
-- Before-snapshot: per-table total / demo / preserved counts (all 22 tables).
-- ===========================================================================
CREATE TEMP TABLE _pre AS
SELECT * FROM (
  SELECT 'users' t, count(*) total, count(*) FILTER (WHERE id = ANY (SELECT id FROM _demo_users) OR email LIKE '%@demo.motivra.app') demo FROM users
  UNION ALL SELECT 'user_roles', count(*), count(*) FILTER (WHERE user_id = ANY (SELECT id FROM _demo_users) OR tenant_id = (SELECT tenant_id FROM _markers)) FROM user_roles
  UNION ALL SELECT 'sessions', count(*), count(*) FILTER (WHERE user_id = ANY (SELECT id FROM _demo_users)) FROM sessions
  UNION ALL SELECT 'verification_tokens', count(*), count(*) FILTER (WHERE user_id = ANY (SELECT id FROM _demo_users)) FROM verification_tokens
  UNION ALL SELECT 'audit_log', count(*), count(*) FILTER (WHERE actor_id = ANY (SELECT id FROM _demo_users) OR tenant_id = (SELECT tenant_id FROM _markers)) FROM audit_log
  UNION ALL SELECT 'vehicles', count(*), count(*) FILTER (WHERE id = ANY (SELECT unnest(vehicle_ids) FROM _markers) OR owner_user_id = ANY (SELECT id FROM _demo_users)) FROM vehicles
  UNION ALL SELECT 'vehicle_components', count(*), count(*) FILTER (WHERE vehicle_id = ANY (SELECT unnest(vehicle_ids) FROM _markers)) FROM vehicle_components
  UNION ALL SELECT 'vehicle_service_history', count(*), count(*) FILTER (WHERE vehicle_id = ANY (SELECT unnest(vehicle_ids) FROM _markers)) FROM vehicle_service_history
  UNION ALL SELECT 'service_requests', count(*), count(*) FILTER (WHERE id = (SELECT request_id FROM _markers) OR customer_id = ANY (SELECT id FROM _demo_users) OR vehicle_id = ANY (SELECT unnest(vehicle_ids) FROM _markers)) FROM service_requests
  UNION ALL SELECT 'jobs', count(*), count(*) FILTER (WHERE id = (SELECT job_id FROM _markers) OR customer_id = ANY (SELECT id FROM _demo_users) OR technician_id = ANY (SELECT id FROM _demo_users) OR vehicle_id = ANY (SELECT unnest(vehicle_ids) FROM _markers) OR service_request_id = (SELECT request_id FROM _markers)) FROM jobs
  UNION ALL SELECT 'job_assignments', count(*), count(*) FILTER (WHERE job_id = (SELECT job_id FROM _markers) OR technician_id = ANY (SELECT id FROM _demo_users) OR assigned_by = ANY (SELECT id FROM _demo_users)) FROM job_assignments
  UNION ALL SELECT 'job_transitions', count(*), count(*) FILTER (WHERE job_id = (SELECT job_id FROM _markers)) FROM job_transitions
  UNION ALL SELECT 'payment_intents', count(*), count(*) FILTER (WHERE id = ANY (SELECT id FROM _demo_intents)) FROM payment_intents
  UNION ALL SELECT 'payment_intent_transitions', count(*), count(*) FILTER (WHERE intent_id = ANY (SELECT id FROM _demo_intents)) FROM payment_intent_transitions
  UNION ALL SELECT 'payment_transactions', count(*), count(*) FILTER (WHERE intent_id = ANY (SELECT id FROM _demo_intents)) FROM payment_transactions
  UNION ALL SELECT 'payment_refunds', count(*), count(*) FILTER (WHERE intent_id = ANY (SELECT id FROM _demo_intents)) FROM payment_refunds
  UNION ALL SELECT 'payment_idempotency_keys', count(*), count(*) FILTER (WHERE key LIKE '%demo%' OR result LIKE '%' || (SELECT job_id FROM _markers)::text) FROM payment_idempotency_keys
  UNION ALL SELECT 'inspection_sessions', count(*), count(*) FILTER (WHERE id = ANY (SELECT id FROM _demo_isessions)) FROM inspection_sessions
  UNION ALL SELECT 'inspection_findings', count(*), count(*) FILTER (WHERE session_id = ANY (SELECT id FROM _demo_isessions)) FROM inspection_findings
  UNION ALL SELECT 'inspection_evidence', count(*), count(*) FILTER (WHERE session_id = ANY (SELECT id FROM _demo_isessions)) FROM inspection_evidence
  UNION ALL SELECT 'inspection_reports', count(*), count(*) FILTER (WHERE session_id = ANY (SELECT id FROM _demo_isessions)) FROM inspection_reports
  UNION ALL SELECT 'inspection_session_transitions', count(*), count(*) FILTER (WHERE session_id = ANY (SELECT id FROM _demo_isessions)) FROM inspection_session_transitions
) s;

-- ===========================================================================
-- Per-step results log
-- ===========================================================================
CREATE TEMP TABLE _log (step text, deleted bigint);

-- ===========================================================================
-- Deletions — FK-safe order, leaves before roots; append-only tables via
-- the sanctioned guarded window (trigger disabled -> delete -> re-enabled;
-- DDL is transactional so any failure restores everything).
-- ===========================================================================

-- 1) identity children of users (no triggers)
WITH d AS (DELETE FROM sessions            WHERE user_id = ANY (SELECT id FROM _demo_users) RETURNING 1)
INSERT INTO _log SELECT 'sessions', count(*) FROM d;
WITH d AS (DELETE FROM verification_tokens WHERE user_id = ANY (SELECT id FROM _demo_users) RETURNING 1)
INSERT INTO _log SELECT 'verification_tokens', count(*) FROM d;

-- 2) payments (intent_transitions is append-only -> guarded window first)
WITH d AS (DELETE FROM payment_refunds        WHERE intent_id = ANY (SELECT id FROM _demo_intents) RETURNING 1)
INSERT INTO _log SELECT 'payment_refunds', count(*) FROM d;
WITH d AS (DELETE FROM payment_transactions   WHERE intent_id = ANY (SELECT id FROM _demo_intents) RETURNING 1)
INSERT INTO _log SELECT 'payment_transactions', count(*) FROM d;
WITH d AS (DELETE FROM payment_idempotency_keys WHERE key LIKE '%demo%' OR result LIKE '%' || (SELECT job_id FROM (SELECT tenant_id, job_id FROM _markers) x)::text RETURNING 1)
INSERT INTO _log SELECT 'payment_idempotency_keys', count(*) FROM d;
ALTER TABLE payment_intent_transitions DISABLE TRIGGER payment_intent_transitions_append_only;
WITH d AS (DELETE FROM payment_intent_transitions WHERE intent_id = ANY (SELECT id FROM _demo_intents) RETURNING 1)
INSERT INTO _log SELECT 'payment_intent_transitions', count(*) FROM d;
ALTER TABLE payment_intent_transitions ENABLE TRIGGER payment_intent_transitions_append_only;
WITH d AS (DELETE FROM payment_intents        WHERE id = ANY (SELECT id FROM _demo_intents) RETURNING 1)
INSERT INTO _log SELECT 'payment_intents', count(*) FROM d;

-- 3) inspections (session_transitions is append-only -> guarded window first)
WITH d AS (DELETE FROM inspection_evidence   WHERE session_id = ANY (SELECT id FROM _demo_isessions) RETURNING 1)
INSERT INTO _log SELECT 'inspection_evidence', count(*) FROM d;
WITH d AS (DELETE FROM inspection_findings   WHERE session_id = ANY (SELECT id FROM _demo_isessions) RETURNING 1)
INSERT INTO _log SELECT 'inspection_findings', count(*) FROM d;
WITH d AS (DELETE FROM inspection_reports    WHERE session_id = ANY (SELECT id FROM _demo_isessions) RETURNING 1)
INSERT INTO _log SELECT 'inspection_reports', count(*) FROM d;
ALTER TABLE inspection_session_transitions DISABLE TRIGGER inspection_session_transitions_append_only;
WITH d AS (DELETE FROM inspection_session_transitions WHERE session_id = ANY (SELECT id FROM _demo_isessions) RETURNING 1)
INSERT INTO _log SELECT 'inspection_session_transitions', count(*) FROM d;
ALTER TABLE inspection_session_transitions ENABLE TRIGGER inspection_session_transitions_append_only;
WITH d AS (DELETE FROM inspection_sessions   WHERE id = ANY (SELECT id FROM _demo_isessions) RETURNING 1)
INSERT INTO _log SELECT 'inspection_sessions', count(*) FROM d;

-- 4) jobs (job_transitions is append-only -> guarded window first)
WITH d AS (DELETE FROM job_assignments WHERE job_id = (SELECT job_id FROM _markers)
                                      OR technician_id = ANY (SELECT id FROM _demo_users)
                                      OR assigned_by   = ANY (SELECT id FROM _demo_users) RETURNING 1)
INSERT INTO _log SELECT 'job_assignments', count(*) FROM d;
ALTER TABLE job_transitions DISABLE TRIGGER job_transitions_append_only;
WITH d AS (DELETE FROM job_transitions WHERE job_id = (SELECT job_id FROM _markers) RETURNING 1)
INSERT INTO _log SELECT 'job_transitions', count(*) FROM d;
ALTER TABLE job_transitions ENABLE TRIGGER job_transitions_append_only;
WITH d AS (DELETE FROM jobs           WHERE id = (SELECT job_id FROM _markers)
                                    OR customer_id = ANY (SELECT id FROM _demo_users)
                                    OR technician_id = ANY (SELECT id FROM _demo_users)
                                    OR vehicle_id = ANY (SELECT unnest(vehicle_ids) FROM _markers)
                                    OR service_request_id = (SELECT request_id FROM _markers) RETURNING 1)
INSERT INTO _log SELECT 'jobs', count(*) FROM d;
WITH d AS (DELETE FROM service_requests WHERE id = (SELECT request_id FROM _markers)
                                      OR customer_id = ANY (SELECT id FROM _demo_users)
                                      OR vehicle_id = ANY (SELECT unnest(vehicle_ids) FROM _markers) RETURNING 1)
INSERT INTO _log SELECT 'service_requests', count(*) FROM d;

-- 5) vehicles (vehicle_service_history is append-only -> guarded window first)
ALTER TABLE vehicle_service_history DISABLE TRIGGER vehicle_service_history_append_only;
WITH d AS (DELETE FROM vehicle_service_history WHERE vehicle_id = ANY (SELECT unnest(vehicle_ids) FROM _markers) RETURNING 1)
INSERT INTO _log SELECT 'vehicle_service_history', count(*) FROM d;
ALTER TABLE vehicle_service_history ENABLE TRIGGER vehicle_service_history_append_only;
WITH d AS (DELETE FROM vehicle_components WHERE vehicle_id = ANY (SELECT unnest(vehicle_ids) FROM _markers) RETURNING 1)
INSERT INTO _log SELECT 'vehicle_components', count(*) FROM d;
WITH d AS (DELETE FROM vehicles WHERE id = ANY (SELECT unnest(vehicle_ids) FROM _markers)
                              OR owner_user_id = ANY (SELECT id FROM _demo_users) RETURNING 1)
INSERT INTO _log SELECT 'vehicles', count(*) FROM d;

-- 6) identity roots (audit_log is append-only -> guarded window last)
WITH d AS (DELETE FROM user_roles WHERE user_id = ANY (SELECT id FROM _demo_users)
                                OR tenant_id = (SELECT tenant_id FROM _markers) RETURNING 1)
INSERT INTO _log SELECT 'user_roles', count(*) FROM d;
WITH d AS (DELETE FROM users WHERE id = ANY (SELECT id FROM _demo_users)
                           OR email LIKE '%@demo.motivra.app' RETURNING 1)
INSERT INTO _log SELECT 'users', count(*) FROM d;
ALTER TABLE audit_log DISABLE TRIGGER audit_log_append_only;
WITH d AS (DELETE FROM audit_log WHERE actor_id = ANY (SELECT id FROM _demo_users)
                               OR tenant_id = (SELECT tenant_id FROM _markers)
                               OR object_id IN (
                                    (SELECT tenant_id FROM _markers)::text,
                                    (SELECT request_id FROM _markers)::text,
                                    (SELECT job_id FROM _markers)::text,
                                    (SELECT assignment_id FROM _markers)::text,
                                    'b0000000-0000-4000-8000-000000000001',
                                    'b0000000-0000-4000-8000-000000000002',
                                    'a0000000-0000-4000-8000-000000000002',
                                    'a0000000-0000-4000-8000-000000000003',
                                    'a0000000-0000-4000-8000-000000000004',
                                    'a0000000-0000-4000-8000-000000000005',
                                    'a0000000-0000-4000-8000-000000000006') RETURNING 1)
INSERT INTO _log SELECT 'audit_log', count(*) FROM d;
ALTER TABLE audit_log ENABLE TRIGGER audit_log_append_only;

-- ===========================================================================
-- After-snapshot + invariant assertions (same transaction as the deletes).
-- ===========================================================================
CREATE TEMP TABLE _post AS
SELECT * FROM (
  SELECT 'users' t, count(*) total, count(*) FILTER (WHERE id = ANY (SELECT id FROM _demo_users) OR email LIKE '%@demo.motivra.app') demo FROM users
  UNION ALL SELECT 'user_roles', count(*), count(*) FILTER (WHERE user_id = ANY (SELECT id FROM _demo_users) OR tenant_id = (SELECT tenant_id FROM _markers)) FROM user_roles
  UNION ALL SELECT 'sessions', count(*), count(*) FILTER (WHERE user_id = ANY (SELECT id FROM _demo_users)) FROM sessions
  UNION ALL SELECT 'verification_tokens', count(*), count(*) FILTER (WHERE user_id = ANY (SELECT id FROM _demo_users)) FROM verification_tokens
  UNION ALL SELECT 'audit_log', count(*), count(*) FILTER (WHERE actor_id = ANY (SELECT id FROM _demo_users) OR tenant_id = (SELECT tenant_id FROM _markers)) FROM audit_log
  UNION ALL SELECT 'vehicles', count(*), count(*) FILTER (WHERE id = ANY (SELECT unnest(vehicle_ids) FROM _markers) OR owner_user_id = ANY (SELECT id FROM _demo_users)) FROM vehicles
  UNION ALL SELECT 'vehicle_components', count(*), count(*) FILTER (WHERE vehicle_id = ANY (SELECT unnest(vehicle_ids) FROM _markers)) FROM vehicle_components
  UNION ALL SELECT 'vehicle_service_history', count(*), count(*) FILTER (WHERE vehicle_id = ANY (SELECT unnest(vehicle_ids) FROM _markers)) FROM vehicle_service_history
  UNION ALL SELECT 'service_requests', count(*), count(*) FILTER (WHERE id = (SELECT request_id FROM _markers) OR customer_id = ANY (SELECT id FROM _demo_users) OR vehicle_id = ANY (SELECT unnest(vehicle_ids) FROM _markers)) FROM service_requests
  UNION ALL SELECT 'jobs', count(*), count(*) FILTER (WHERE id = (SELECT job_id FROM _markers) OR customer_id = ANY (SELECT id FROM _demo_users) OR technician_id = ANY (SELECT id FROM _demo_users) OR vehicle_id = ANY (SELECT unnest(vehicle_ids) FROM _markers) OR service_request_id = (SELECT request_id FROM _markers)) FROM jobs
  UNION ALL SELECT 'job_assignments', count(*), count(*) FILTER (WHERE job_id = (SELECT job_id FROM _markers) OR technician_id = ANY (SELECT id FROM _demo_users) OR assigned_by = ANY (SELECT id FROM _demo_users)) FROM job_assignments
  UNION ALL SELECT 'job_transitions', count(*), count(*) FILTER (WHERE job_id = (SELECT job_id FROM _markers)) FROM job_transitions
  UNION ALL SELECT 'payment_intents', count(*), count(*) FILTER (WHERE id = ANY (SELECT id FROM _demo_intents)) FROM payment_intents
  UNION ALL SELECT 'payment_intent_transitions', count(*), count(*) FILTER (WHERE intent_id = ANY (SELECT id FROM _demo_intents)) FROM payment_intent_transitions
  UNION ALL SELECT 'payment_transactions', count(*), count(*) FILTER (WHERE intent_id = ANY (SELECT id FROM _demo_intents)) FROM payment_transactions
  UNION ALL SELECT 'payment_refunds', count(*), count(*) FILTER (WHERE intent_id = ANY (SELECT id FROM _demo_intents)) FROM payment_refunds
  UNION ALL SELECT 'payment_idempotency_keys', count(*), count(*) FILTER (WHERE key LIKE '%demo%' OR result LIKE '%' || (SELECT job_id FROM (SELECT tenant_id, job_id FROM _markers) x)::text) FROM payment_idempotency_keys
  UNION ALL SELECT 'inspection_sessions', count(*), count(*) FILTER (WHERE id = ANY (SELECT id FROM _demo_isessions)) FROM inspection_sessions
  UNION ALL SELECT 'inspection_findings', count(*), count(*) FILTER (WHERE session_id = ANY (SELECT id FROM _demo_isessions)) FROM inspection_findings
  UNION ALL SELECT 'inspection_evidence', count(*), count(*) FILTER (WHERE session_id = ANY (SELECT id FROM _demo_isessions)) FROM inspection_evidence
  UNION ALL SELECT 'inspection_reports', count(*), count(*) FILTER (WHERE session_id = ANY (SELECT id FROM _demo_isessions)) FROM inspection_reports
  UNION ALL SELECT 'inspection_session_transitions', count(*), count(*) FILTER (WHERE session_id = ANY (SELECT id FROM _demo_isessions)) FROM inspection_session_transitions
) s;

DO $$
BEGIN
  -- Invariant 1: demo counts must be ZERO everywhere after the purge.
  IF EXISTS (SELECT 1 FROM _post WHERE demo <> 0) THEN
    RAISE EXCEPTION 'demo rows remain after purge: %',
      (SELECT string_agg(t || '=' || demo, ', ') FROM _post WHERE demo <> 0);
  END IF;
  -- Invariant 2: preserved (non-demo) counts must be IDENTICAL pre/post.
  IF EXISTS (
    SELECT 1 FROM _pre p JOIN _post q USING (t)
    WHERE (p.total - p.demo) <> (q.total - q.demo)
  ) THEN
    RAISE EXCEPTION 'non-demo rows changed during purge: %',
      (SELECT string_agg(p.t || ': ' || (p.total - p.demo) || ' -> ' || (q.total - q.demo), ', ')
         FROM _pre p JOIN _post q USING (t)
        WHERE (p.total - p.demo) <> (q.total - q.demo));
  END IF;
END $$;

-- ===========================================================================
-- Evidence report (before -> after)
-- ===========================================================================
SELECT p.t AS table_name,
       p.total  AS before_total,
       p.demo   AS before_demo,
       q.total  AS after_total,
       q.demo   AS after_demo,
       (p.total - p.demo) AS preserved
FROM _pre p JOIN _post q USING (t)
ORDER BY p.t;

SELECT l.step AS deleted_from, l.deleted AS rows_deleted
FROM _log l WHERE l.deleted > 0 ORDER BY l.deleted DESC;

SELECT 'demo purge complete: all demo counts zero, preserved counts unchanged, triggers re-armed' AS status;
SQL

rc=$?
if [[ $rc -eq 0 ]]; then
  echo "Demo purge committed. Next steps:"
  echo "  1. make integrity-check DB_URL=\"\${DB_URL:-$CONN}\"   # must be all-clear"
  echo "  2. record the before/after table above in the operations log"
else
  echo "Purge FAILED (exit $rc) — transaction rolled back, database unchanged." >&2
fi
exit $rc
