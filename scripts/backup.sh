#!/usr/bin/env bash
#
# backup.sh — consistent plain-SQL-free custom-format dump of the Motivra
# PostgreSQL database (integrity issue #83).
#
# Usage:
#   scripts/backup.sh [CONNECTION_URL] [OUTPUT_DIR]
#
# Resolution order for the URL: $1 > MOTIVRA_DATABASE_URL > app-stack
# default (127.0.0.1:5433). Output: OUTPUT_DIR or ./backups, file name
# motivra-YYYYmmddTHHMMSSZ.dump (pg_dump custom format, compressed).
#
# The custom format restores selectively and in parallel (pg_restore -j);
# scripts/restore.sh consumes it. Retention of backup files is an operator
# policy — see docs/RUNBOOK.md (Backup & DR).

set -euo pipefail

CONN="${1:-${MOTIVRA_DATABASE_URL:-postgres://motivra:motivra@127.0.0.1:5433/motivra?sslmode=disable}}"
OUT_DIR="${2:-./backups}"

command -v pg_dump >/dev/null 2>&1 || {
  echo "error: pg_dump not found on PATH (install postgresql-client)" >&2
  exit 1
}
mkdir -p "$OUT_DIR"

STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
TARGET="$OUT_DIR/motivra-$STAMP.dump"

echo "dumping to $TARGET ..."
pg_dump --format=custom --no-owner --no-privileges --file "$TARGET" "$CONN"

SIZE="$(du -h "$TARGET" | cut -f1)"
echo "backup complete: $TARGET ($SIZE)"
echo ""
echo "Verify it restores (do not trust untested backups):"
echo "  scripts/restore.sh '$TARGET' <scratch-database-url>"
