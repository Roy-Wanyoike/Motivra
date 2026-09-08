# Migrations — identity

golang-migrate chain for the identity domain: paired NNNNNN_name.up.sql / NNNNNN_name.down.sql files, validated by scripts/validate_migrations.sh. Backward-compatible changes only (expand, then migrate, then contract).

Owned by the identity domain owner; see docs/ARCHITECTURE.md §2 — do not edit outside your zone.
