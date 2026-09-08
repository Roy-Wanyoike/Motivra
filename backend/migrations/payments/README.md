# Migrations — payments

golang-migrate chain for the payments domain: paired NNNNNN_name.up.sql / NNNNNN_name.down.sql files, validated by scripts/validate_migrations.sh. Backward-compatible changes only (expand, then migrate, then contract).

Owned by the payments domain owner; see docs/ARCHITECTURE.md §2 — do not edit outside your zone.
