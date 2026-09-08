#!/usr/bin/env bash
#
# validate_migrations.sh — CI gate for the per-domain migration chains.
#
# Rules (see docs/ARCHITECTURE.md §9 — migrations are partitioned per domain):
#   - Every domain directory under backend/migrations/<domain>/ is validated.
#   - Filenames must match ^[0-9]{6}_[a-z0-9_]+\.(up|down)\.sql$
#   - Every N_name.up.sql must have a matching N_name.down.sql (and vice versa).
#   - Up-migration numbers must start at 000001 and be strictly sequential with
#     no gaps and no duplicates.
#   - Domains with zero migrations are valid (the chain simply has not started).
#
# Usage: bash scripts/validate_migrations.sh  (also wired as `make validate-migrations`)

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MIGRATIONS_DIR="$REPO_ROOT/backend/migrations"

if [[ ! -d "$MIGRATIONS_DIR" ]]; then
	echo "migration validation: FAIL (missing directory: backend/migrations)" >&2
	exit 1
fi

violation_count=0
domain_count=0
migration_count=0

fail() {
	echo "migration validation: violation in domain '$1': $2" >&2
	violation_count=$((violation_count + 1))
}

for domain_dir in "$MIGRATIONS_DIR"/*/; do
	[[ -d "$domain_dir" ]] || continue
	domain="$(basename "$domain_dir")"
	domain_count=$((domain_count + 1))

	declare -A ups_seen=() downs_seen=()
	up_keys=()
	down_keys=()

	# C-locale sort keeps the numeric prefix order stable and deterministic.
	while IFS= read -r path; do
		name="$(basename "$path")"
		case "$name" in
		*.up.sql)
			key="${name%.up.sql}"
			if [[ ! "$name" =~ ^[0-9]{6}_[a-z0-9_]+\.up\.sql$ ]]; then
				fail "$domain" "filename does not match ^[0-9]{6}_[a-z0-9_]+.up.sql: $name"
				continue
			fi
			if [[ -n "${ups_seen[$key]:-}" ]]; then
				fail "$domain" "duplicate migration: $name"
				continue
			fi
			ups_seen["$key"]=1
			up_keys+=("$key")
			;;
		*.down.sql)
			key="${name%.down.sql}"
			if [[ ! "$name" =~ ^[0-9]{6}_[a-z0-9_]+\.down\.sql$ ]]; then
				fail "$domain" "filename does not match ^[0-9]{6}_[a-z0-9_]+.down.sql: $name"
				continue
			fi
			if [[ -n "${downs_seen[$key]:-}" ]]; then
				fail "$domain" "duplicate migration: $name"
				continue
			fi
			downs_seen["$key"]=1
			down_keys+=("$key")
			;;
		*)
			fail "$domain" "file is not a .up.sql/.down.sql pair member: $name"
			;;
		esac
	done < <(find "$domain_dir" -maxdepth 1 -type f -name '*.sql' | LC_ALL=C sort)

	for key in "${up_keys[@]}"; do
		if [[ -z "${downs_seen[$key]:-}" ]]; then
			fail "$domain" "up migration without matching down migration: $key.up.sql"
		fi
	done

	for key in "${down_keys[@]}"; do
		if [[ -z "${ups_seen[$key]:-}" ]]; then
			fail "$domain" "down migration without matching up migration: $key.down.sql"
		fi
	done

	expected=1
	for key in "${up_keys[@]}"; do
		num="$((10#${key%%_*}))"
		if ((num < expected)); then
			fail "$domain" "non-sequential migration number $key (expected $(printf '%06d' "$expected") or later)"
		elif ((num > expected)); then
			fail "$domain" "gap in migration numbering: $key follows $(printf '%06d' "$((expected - 1))")"
		fi
		expected=$((num + 1))
		migration_count=$((migration_count + 1))
	done

	unset ups_seen downs_seen
done

if ((violation_count > 0)); then
	echo "migration validation: FAILED ($violation_count violation(s) across $domain_count domain(s))" >&2
	exit 1
fi

echo "migration validation: OK ($domain_count domains, $migration_count migrations)"
