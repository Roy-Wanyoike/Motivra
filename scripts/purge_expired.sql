-- purge_expired.sql — retention enforcement for PII-bearing operational
-- tables (integrity issue #83; policy documented in docs/RUNBOOK.md and
-- docs/PII-HANDLING.md).
--
-- Removes:
--   1. Refresh sessions expired or revoked more than grace_days ago
--      (default 30) — rows retain no operational value past that point
--      and carry IP addresses and device names (Kenya DPA 2019 scope).
--   2. audit_log entries older than audit_retention_months (default 18),
--      ONLY when purge_audit=true. audit_log is append-only (identity
--      migration 000006): this script's guarded disable/enable window is
--      the single sanctioned delete path, and every invocation must be
--      recorded in the operations log.
--
-- Usage (psql variables):
--   psql "$MOTIVRA_DATABASE_URL" \
--     -v grace_days=30 -v audit_retention_months=18 -v purge_audit=false \
--     -f scripts/purge_expired.sql
--
-- DRY RUN DEFAULT: with purge_audit=false audit rows are counted, not
-- deleted.

\set ON_ERROR_STOP on

-- Expose the psql variables as session settings so the DO block below can
-- read them (psql :var substitution does not reach dollar-quoted bodies).
SET motivra.grace_days = COALESCE(NULLIF(:'grace_days', ''), '30');
SET motivra.audit_retention_months = COALESCE(NULLIF(:'audit_retention_months', ''), '18');
SET motivra.purge_audit = COALESCE(NULLIF(:'purge_audit', ''), 'false');

-- 1) Sessions: safe to delete unconditionally (no append-only trigger).
WITH purged AS (
    DELETE FROM sessions
    WHERE revoked_at < now() - (current_setting('motivra.grace_days') || ' days')::interval
       OR expires_at < now() - (current_setting('motivra.grace_days') || ' days')::interval
       OR absolute_expires_at < now() - (current_setting('motivra.grace_days') || ' days')::interval
    RETURNING 1
)
SELECT 'sessions_purged' AS step, count(*) AS rows FROM purged;

-- 2) Audit retention (trigger-guarded; opt-in per invocation).
DO $$
DECLARE
    eligible bigint;
BEGIN
    IF current_setting('motivra.purge_audit') = 'true' THEN
        SELECT count(*) INTO eligible FROM audit_log
        WHERE created_at < now() - (current_setting('motivra.audit_retention_months')::int || ' months')::interval;

        IF eligible > 0 THEN
            ALTER TABLE audit_log DISABLE TRIGGER audit_log_append_only;
            DELETE FROM audit_log
            WHERE created_at < now() - (current_setting('motivra.audit_retention_months')::int || ' months')::interval;
            ALTER TABLE audit_log ENABLE TRIGGER audit_log_append_only;
            RAISE NOTICE 'audit_log purged: % rows older than % months (trigger re-armed)',
                eligible, current_setting('motivra.audit_retention_months');
        ELSE
            RAISE NOTICE 'audit_log: nothing older than the retention window';
        END IF;
    ELSE
        SELECT count(*) INTO eligible FROM audit_log
        WHERE created_at < now() - (current_setting('motivra.audit_retention_months')::int || ' months')::interval;
        RAISE NOTICE 'DRY RUN: % audit_log rows are purge-eligible (re-run with -v purge_audit=true)', eligible;
    END IF;
END $$;
