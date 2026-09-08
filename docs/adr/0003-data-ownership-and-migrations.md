# ADR-0003: Data Ownership and Per-Domain Migration Strategy

- **Status:** Accepted
- **Date:** 2026-09-08
- **Deciders:** Principal Architect (arch-01), per issue #2
- **Related:** ADR-0001 (bounded contexts), ADR-0002 (events), ADR-0004 (security/tenant isolation), `docs/ARCHITECTURE.md` §2, §9

## Context

Motivra's domain model has an unusually high density of integrity-critical rules: vehicle history is append-only, the payments ledger is authoritative and must never be mutable, tenant isolation is a security boundary, and evidence provenance must survive audits. At the same time, the build is executed by many agents and engineers in parallel, all committing to one repository (ADR-0001).

The central tension: one shared PostgreSQL cluster is the pragmatic choice at 0→1 (one backup story, one connection topology, transactional outbox for ADR-0002), but a shared database is the classic way bounded contexts erode — first a cross-domain `JOIN`, then a shared table, then nobody can change anything without a negotiation.

What must be decided:

1. The authoritative store and its scope (what is and is never a source of truth).
2. How schema ownership is physically partitioned so cross-domain table access is visible.
3. How migration chains are organized so parallel owners never collide on a shared resource.
4. The compatibility rules for schema evolution, especially destructive changes.

## Decision

**PostgreSQL (with PostGIS) is the sole source of truth** for all persistent domain data: identity, vehicles, history, jobs, dispatch, estimates, payments, parts, garages, notifications, fleet, warranties. PostGIS provides the geospatial substrate for dispatch (service areas, technician location, distance scoring). Caching (Redis), search (OpenSearch) and analytics (ClickHouse) are *derivations*: any of them can be rebuilt from PostgreSQL plus the event log without loss.

Hard rules:

1. **Redis is never a source of truth** — for money, history, permissions or any state whose loss is more than an inconvenience. It holds caches, rate-limit counters and ephemeral coordination only.
2. **History is append-only where the domain says so**: `vehicle_service_history`, evidence records, ledger entries. Corrections are new records that reference the original; UPDATE/DELETE on those tables is a review-blocking violation (enforced by review, and by DB role privileges where practical).
3. **No cross-domain table access.** A context's service may only read/write tables in its own domain's schema/table namespace. Cross-context needs go through APIs, commands or versioned events (ADR-0002). Each domain service connects with its **own database role** where possible, granted privileges only on its own tables — making cross-domain SQL fail loudly rather than silently.

### Migration chains

Migrations live under `/backend/migrations/<domain>/`, one chain per domain, using **golang-migrate-compatible sequential naming**:

```text
/backend/migrations/identity/000001_create_users.up.sql
/backend/migrations/identity/000001_create_users.down.sql
/backend/migrations/identity/000002_add_sessions.up.sql
/backend/migrations/identity/000002_add_sessions.down.sql
/backend/migrations/vehicles/000001_create_vehicles.up.sql
/backend/migrations/vehicles/000001_create_vehicles.down.sql
```

Rules:

1. **Per-domain chains, zero shared files.** No domain ever edits another domain's migration directory; there is no cross-domain "common" chain. Schema-level shared objects (e.g. the `processed_events` table from ADR-0002) are owned by exactly one named domain (the producer of the concern) and documented in `/contracts/events/README.md` or the platform docs.
2. **Every migration ships as an up/down pair**, and the down path must be tested (applied and reversed in CI, not merely present).
3. **Backward-compatible by default.** A normal deploy may contain only migrations that the currently-running previous code version tolerates: add columns nullable or with defaults, add new tables, add indexes `CONCURRENTLY` where the table is hot. Renames, narrowing types, dropping columns are destructive — see below.
4. **Destructive changes follow expand → migrate → contract:**
   - *Expand*: ship the new shape alongside the old (new column populated by code, dual-writes where unavoidable).
   - *Migrate*: backfill and switch reads/writes in application code; deploy and soak.
   - *Contract*: only after the old shape is provably unused (logs, metrics), ship the removal migration — and even it must be reversible to the expand state.
   - Contract steps never ride along with unrelated changes; they are separate, clearly titled migrations.
5. **Migrations are forward-only in deploys.** The down path exists for rollback drills and local development; production rollback preference is "roll forward with a fix" or "revert the deploy before the migration applies", and migrations run as a distinct deploy step so a bad migration does not auto-apply with an application rollback.
6. **sqlc over ORM.** Queries are explicit SQL managed with sqlc/pgx (per the README stack table). Schemas are owned by these migration files, never by an ORM's auto-sync.

### CI enforcement

`scripts/validate_migrations.sh` runs in CI on every PR touching `/backend/migrations/`: it verifies sequential numbering per domain, that every `.up.sql` has a matching `.down.sql`, that filenames match the pattern `NNNNNN_name.up|down.sql`, and (with a disposable Postgres service) that each changed chain applies cleanly from empty and reverses cleanly. It additionally flags `DROP TABLE`/`DROP COLUMN` outside migrations explicitly titled as contract steps.

## Alternatives considered

**Single shared migration chain** (`/backend/migrations/` flat, one global sequence). Rejected: it is a high-risk shared resource under a multi-agent build — every PR touching schema conflicts textually at the head revision, sequential global numbering serializes merges, and ownership of any table becomes ambiguous exactly where the domain model demands the opposite. Rejected even though it is operationally simpler for a single-tenant database: the per-domain directory mapping gives the same outcome (one cluster, ordered per-domain chains) without the collision surface.

**Database-per-context (separate clusters or schemas with separate connection planes).** The strongest isolation — cross-domain access becomes impossible rather than forbidden. Rejected for now: it multiplies backup/restore, observability, migration tooling and transactional-outbox complexity at the stage when the team is smallest. The per-domain DB-role rule keeps the exit door open; revisit if a domain's load profile (e.g. analytics ingestion) or compliance posture diverges enough to justify it.

**ORM-managed schemas** (GORM/Ent auto-migration, or Django-style model sync). Rejected: the schema stops being a reviewable artifact — DDL is generated from scattered code state, diffs are opaque, destructive changes happen implicitly, and the expand→migrate→contract discipline cannot be expressed or audited. Explicit SQL files keep every schema change reviewable, reversible and attributable — which matters more than authoring convenience for a system whose history and ledger are legal-ish records.

**NoSQL-first for history/evidence.** Rejected: document stores trade away the transactional guarantees the ledger and state machines need, and the geospatial requirements would end up bolted on anyway. JSONB within PostgreSQL covers the flexible-shape cases when justified.

## Consequences

**Positive**

- Schema changes are reviewable, attributable, and owned: any migration file's path names its owner, and CI validates its pair and reversibility.
- Parallel agents work without colliding — the migration chains are disjoint by construction.
- The source-of-truth hierarchy (PostgreSQL authoritative; Redis/OpenSearch/ClickHouse rebuildable derivations) makes disaster recovery tractable: restore Postgres, replay streams, rebuild the rest.
- DB-role partitioning turns cross-domain table access from a policy violation into a runtime error — self-enforcing, testable in integration tests (with ADR-0004 tenant-isolation tests).

**Negative / costs**

- Per-domain roles add operational configuration (roles, grants per service) and one more thing to script in Wave 1 platform work.
- Some analytic queries that would be trivial as a cross-domain JOIN now require consuming events or calling APIs — accepted deliberately; the analytics context's projections exist for exactly this.
- Migration discipline (tested down paths, expand/contract staging) costs authoring time on every schema change; the validate script and CI catch skips mechanically.
- `CONCURRENTLY` index builds cannot run inside transactions, which constrains how migration runners batch steps; the platform's migration runner must accommodate non-transactional migrations where flagged.
