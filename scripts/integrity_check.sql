-- integrity_check.sql — referential-integrity verification for the Motivra
-- schema (integrity issue #83). Run after any destructive operation, before
-- and after demo seeding on shared environments, and on a schedule in
-- production (see RUNBOOK). A healthy database reports zero orphans and
-- the per-table row counts this script prints.
--
-- Usage:
--   psql "$MOTIVRA_DATABASE_URL" -f scripts/integrity_check.sql
--
-- Exit code: psql exits non-zero when any of the failing checks selects
-- rows, thanks to \if error handling via ON_ERROR_STOP + DO blocks below.

\set ON_ERROR_STOP on

-- Per-table record counts (the before/after evidence for any cleanup).
SELECT 'users' AS table, count(*) AS rows FROM users
UNION ALL SELECT 'user_roles', count(*) FROM user_roles
UNION ALL SELECT 'sessions', count(*) FROM sessions
UNION ALL SELECT 'audit_log', count(*) FROM audit_log
UNION ALL SELECT 'vehicles', count(*) FROM vehicles
UNION ALL SELECT 'vehicle_components', count(*) FROM vehicle_components
UNION ALL SELECT 'vehicle_service_history', count(*) FROM vehicle_service_history
UNION ALL SELECT 'service_requests', count(*) FROM service_requests
UNION ALL SELECT 'jobs', count(*) FROM jobs
UNION ALL SELECT 'job_assignments', count(*) FROM job_assignments
UNION ALL SELECT 'job_transitions', count(*) FROM job_transitions
ORDER BY 1;

DO $$
DECLARE
    orphans bigint;
BEGIN
    -- 1) Sessions must reference live users.
    SELECT count(*) INTO orphans FROM sessions s
    LEFT JOIN users u ON u.id = s.user_id WHERE u.id IS NULL;
    IF orphans > 0 THEN RAISE EXCEPTION 'orphan sessions: %', orphans; END IF;

    -- 2) Role grants must reference live users.
    SELECT count(*) INTO orphans FROM user_roles r
    LEFT JOIN users u ON u.id = r.user_id WHERE u.id IS NULL;
    IF orphans > 0 THEN RAISE EXCEPTION 'orphan user_roles: %', orphans; END IF;

    -- 3) Vehicle history must reference live vehicles.
    SELECT count(*) INTO orphans FROM vehicle_service_history h
    LEFT JOIN vehicles v ON v.id = h.vehicle_id WHERE v.id IS NULL;
    IF orphans > 0 THEN RAISE EXCEPTION 'orphan vehicle_service_history: %', orphans; END IF;

    -- 4) Components must reference live vehicles.
    SELECT count(*) INTO orphans FROM vehicle_components c
    LEFT JOIN vehicles v ON v.id = c.vehicle_id WHERE v.id IS NULL;
    IF orphans > 0 THEN RAISE EXCEPTION 'orphan vehicle_components: %', orphans; END IF;

    -- 5) Jobs must reference a live customer.
    SELECT count(*) INTO orphans FROM jobs j
    LEFT JOIN users cu ON cu.id = j.customer_id WHERE cu.id IS NULL;
    IF orphans > 0 THEN RAISE EXCEPTION 'orphan jobs (customer): %', orphans; END IF;

    -- 6) Job transitions must reference live jobs.
    SELECT count(*) INTO orphans FROM job_transitions t
    LEFT JOIN jobs j ON j.id = t.job_id WHERE j.id IS NULL;
    IF orphans > 0 THEN RAISE EXCEPTION 'orphan job_transitions: %', orphans; END IF;

    -- 7) Job assignments must reference live jobs.
    SELECT count(*) INTO orphans FROM job_assignments a
    LEFT JOIN jobs j ON j.id = a.job_id WHERE j.id IS NULL;
    IF orphans > 0 THEN RAISE EXCEPTION 'orphan job_assignments: %', orphans; END IF;

    -- 8) Every job that has transitions must declare the latest one's
    --    target status as its current status.
    SELECT count(*) INTO orphans FROM jobs j
    WHERE EXISTS (SELECT 1 FROM job_transitions t WHERE t.job_id = j.id)
      AND j.status <> (
          SELECT t.to_status FROM job_transitions t
          WHERE t.job_id = j.id
          ORDER BY t.created_at DESC, t.id DESC
          LIMIT 1
      );
    IF orphans > 0 THEN RAISE EXCEPTION 'jobs whose status diverges from transition trail: %', orphans; END IF;

    -- 9) Append-only enforcement must be armed.
    IF NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname = 'audit_log_append_only' AND NOT tgisinternal) THEN
        RAISE EXCEPTION 'audit_log_append_only trigger missing';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname = 'job_transitions_append_only' AND NOT tgisinternal) THEN
        RAISE EXCEPTION 'job_transitions_append_only trigger missing';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname = 'vehicle_service_history_append_only' AND NOT tgisinternal) THEN
        RAISE EXCEPTION 'vehicle_service_history_append_only trigger missing';
    END IF;

    RAISE NOTICE 'integrity check: all orphan counts are zero, append-only triggers armed';
END $$;
