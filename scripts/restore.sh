#!/usr/bin/env bash
#
# restore.sh — restore a backup.sh dump into a target database (integrity
# issue #83).
#
# Usage:
#   I_UNDERSTAND_THIS_DROPS_DATA=1 scripts/restore.sh <backup.dump> [CONNECTION_URL]
#
# The target database is DROPPED AND RECREATED from the dump. The explicit
# environment opt-in prevents a stray command from destroying live data;
# restoring into production therefore requires deliberately setting the
# flag against the production URL.
#
# URL resolution: $2 > MOTIVRA_DATABASE_URL > app-stack default.

set -euo pipefail

BACKUP="${1:-}"
CONN="${2:-${MOTIVRA_DATABASE_URL:-postgres://motivra:motivra@127.0.0.1:5433/motivra?sslmode=disable}}"

if [[ -z "$BACKUP" || ! -f "$BACKUP" ]]; then
  echo "usage: I_UNDERSTAND_THIS_DROPS_DATA=1 scripts/restore.sh <backup.dump> [CONNECTION_URL]" >&2
  exit 2
fi
if [[ "${I_UNDERSTAND_THIS_DROPS_DATA:-0}" != "1" ]]; then
  echo "refusing to restore: this DROPS the target database and recreates it from $BACKUP" >&2
  echo "re-run with I_UNDERSTAND_THIS_DROPS_DATA=1 when the target is confirmed" >&2
  exit 3
fi

command -v pg_restore >/dev/null 2>&1 || {
  echo "error: pg_restore not found on PATH (install postgresql-client)" >&2
  exit 1
}

JOBS="$(nproc 2>/dev/null || echo 2)"
echo "restoring $BACKUP into ${CONN%%\?*} (parallel -j$JOBS) ..."
pg_restore --clean --if-exists --no-owner --no-privileges --jobs "$JOBS" --dbname "$CONN" "$BACKUP"
echo "restore complete."
echo ""
echo "Post-restore integrity gate (required before serving traffic):"
echo "  scripts/integrity_check.sql executed via: make integrity-check"
