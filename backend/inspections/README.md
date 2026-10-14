# Backend — Inspections

Template-driven inspection sessions, GREEN/AMBER/RED findings, evidence
provenance and verifiable inspection reports (issue #61, Wave 3 A09+A10).
See docs/ARCHITECTURE.md §2 for the context boundaries and ADR-0003 for the
per-domain migration chain.

## Session state machine

Every status move passes `CanTransition(from, to)`; the state machine is
the single source of truth (`statemachine.go`) and nothing may write a
session status without going through it. The machine is strictly linear —
there is deliberately no way back: a corrected inspection is a NEW session,
mirroring the vehicles corrections-as-new-records precedent.

```
CREATED -> IN_PROGRESS -> SUBMITTED -> REPORT_AVAILABLE
```

- CREATED -> IN_PROGRESS: the inspector started the walk (`started_at`
  stamped). Findings and evidence are recordable only while IN_PROGRESS —
  the database triggers refuse them in every other state.
- IN_PROGRESS -> SUBMITTED: the inspector attests completion; report
  integrity preconditions are validated first (422 naming every violated
  precondition), then findings and evidence are sealed.
- SUBMITTED -> REPORT_AVAILABLE: the verifiable report is assembled and
  issued.
- REPORT_AVAILABLE is terminal and immutable.

### Guard rules

- No findings or evidence outside IN_PROGRESS: the service refuses with
  `ErrSessionSealed` and the `inspection_findings_sealed_after_submit` /
  `inspection_evidence_sealed_after_submit` triggers enforce the same rule
  at the database level.
- Submit is not assembly: `Submit` validates readiness (every required
  template item has a finding, at least one evidence record exists, every
  RED finding is linked to evidence) and `IssueReport` re-checks the same
  preconditions — defense in depth.
- Only the session's inspector (or a control-plane operator) works a
  session: `guardActor` keeps staff members out of sessions they do not
  inspect even when their scope can read them.
- Concurrent movers lose cleanly: `UpdateSessionStatus` re-checks the
  expected status under `FOR UPDATE` and conflicts with 409.

### Append-only transition log

`inspection_session_transitions` records every successful move
(`from_status`, `to_status`, `actor_id`, `reason`) in the same transaction
as the status update. Rows are never updated or deleted (append-only
trigger, same discipline as `job_transitions`).

## Findings: severity model and corrections

Findings carry a result (`PASS`, `FAIL`, `ATTENTION`, `NOT_APPLICABLE`)
and a traffic-light severity (`GREEN`, `AMBER`, `RED`). The legal
combinations are pinned in `service.go`:

| Result | Allowed severities |
|---|---|
| PASS | GREEN, AMBER (a passing item may carry an observation) |
| FAIL | AMBER, RED |
| ATTENTION | AMBER, RED |
| NOT_APPLICABLE | GREEN |

Rows are append-only: a corrected item result is a NEW record (the
database trigger refuses UPDATE/DELETE outright) and the newest record per
item key wins at report assembly (`SelectLatestFindings`).

## Evidence provenance

An evidence record certifies that a piece of media (PHOTO, READING,
DOCUMENT) existed at `captured_at` under `captured_by`: it stores the
lowercase hex SHA-256 of the content (`content_hash`), the capture
timestamp, the capturer (always the authenticated actor, never client
input) and the client-attested `uploaded` flag. An optional `finding_id`
links evidence to a specific finding. **No media storage lives here** —
records reference hashes only; the storage adapter is a follow-up.

## Report assembly and verification

`IssueReport` snapshots the newest finding per item plus the full evidence
set, hashes the canonical form into `content_hash` (the exact algorithm is
documented at the top of `report.go`; notes are hashed, never inlined, so
free text cannot forge canonical lines) and inserts the append-only report
in the same transaction that moves the session to REPORT_AVAILABLE.

`CheckReportReadiness` refuses assembly when integrity preconditions fail:
a required template item without a finding, an unknown item key, a RED
finding without linked evidence, or **no evidence records at all — reports
are never assembled with missing evidence hashes** (database trigger
`inspection_reports_require_evidence` enforces the same floor).

`Verify` recomputes the canonical hash from the live rows and reports
`verified: false` with per-check flags (`hash_match`, `findings_match`,
`evidence_match`, `template_match`) instead of erroring — tampering is a
verdict, not an exception.

## HTTP API (v1)

| Method | Path | Who | Purpose |
|---|---|---|---|
| POST | /v1/inspections/sessions | staff roles | create a session (template resolved + frozen) |
| GET | /v1/inspections/sessions?status=&vehicle_id=&job_id=&limit=&offset= | staff roles | list scoped sessions |
| GET | /v1/inspections/sessions/{id} | authenticated | fetch one session (scoped) |
| POST | /v1/inspections/sessions/{id}/start | staff roles | CREATED -> IN_PROGRESS |
| POST | /v1/inspections/sessions/{id}/findings | staff roles | record a finding |
| GET | /v1/inspections/sessions/{id}/findings | authenticated | list the finding log |
| POST | /v1/inspections/sessions/{id}/evidence | staff roles | attach an evidence reference (hash + provenance) |
| GET | /v1/inspections/sessions/{id}/evidence | authenticated | list evidence records |
| POST | /v1/inspections/sessions/{id}/submit | staff roles | submit + seal (readiness validated) |
| POST | /v1/inspections/sessions/{id}/report | staff roles | assemble + issue the report |
| GET | /v1/inspections/sessions/{id}/report | authenticated | fetch the report |
| GET | /v1/inspections/sessions/{id}/report/verification | authenticated | recompute report integrity |
| GET | /v1/inspections/templates?vehicle_class= | authenticated | list the template registry |
| GET | /v1/inspections/templates/{id} | authenticated | fetch one template |

Staff roles: TECHNICIAN, GARAGE, DISPATCHER, ADMIN, SUPER_ADMIN
(`platform.RequireRole`); CUSTOMER reads their own inspections and reports
through the `customer_id` linkage. Contract:
`contracts/inspections/openapi.yaml`. Errors are problem+json via
`platform.WriteError` (401/403/404/409/422 mapped from the domain
sentinels `ErrConflict`, `ErrSessionSealed`, `ErrReportIncomplete`,
`ErrInvalidInput`, `ErrNotInspector`, `ErrSessionNotFound`,
`ErrReportNotFound`).

## Events (JetStream, domain `inspections`)

| Event | Emitted when |
|---|---|
| `session.created.v1` | a session is created (template snapshot included) |
| `session.started.v1` | the inspector starts the walk |
| `session.submitted.v1` | the session is sealed (payload carries findings/evidence counts) |
| `report.issued.v1` | the verifiable report is issued (payload carries the content hash) |

Subjects follow `motivra.inspections.<aggregate>.<event>.v1` (ADR-0002).
`cmd/inspections` constructs a `platform.NATSPublisher` when
`MOTIVRA_NATS_URL` is set; while unset the service runs with a nil
publisher and `Service` skips publishing. Every publish attempt is counted
(success/failure) on the domain metrics; a failed publish surfaces to the
caller — events are part of the domain contract, not best-effort logging.

## Observability

Domain instruments register on the platform registry
(`platform.SetupMetrics().Registry()`, ADR-0005):

| Metric | Kind | Meaning |
|---|---|---|
| `inspections_sessions_created_total` | counter | sessions created |
| `inspections_sessions_submitted_total` | counter | sessions submitted (sealed) |
| `inspections_reports_issued_total` | counter | verifiable reports issued |
| `inspections_report_assembly_seconds` | histogram | report-assembly latency |
| `inspections_events_published_total{event_type,outcome}` | counter | event publish success/failure |

## Service wiring

`cmd/inspections/main.go`: config -> logger -> tracing/metrics ->
Postgres pool ->
`platform.MigrateUp(inspectionsmigrations.FS, "schema_migrations_inspections")`
-> `inspections.NewMetrics(metrics.Registry())` -> optional NATS publisher
-> `inspections.NewPostgresStore` -> `inspections.NewService` -> server
with `platform.AuthMiddleware` -> `inspections.Routes` -> graceful
shutdown. The database is required at boot (reports are authoritative).

## Real vs stubbed

| Capability | Status | Notes |
|---|---|---|
| Template registry (6 vehicle classes, uuid-v5 deterministic ids) | real | code-owned system content; sessions/reports snapshot the resolved template |
| Session state machine + transition audit | real | exhaustive matrix tested; audit rows append-only |
| Findings model (result x severity matrix) | real | DB CHECK constraints mirror the code matrix |
| Evidence provenance (hash, captured-at/by, uploaded) | real | sha256 content hashes; DB CHECK enforces the hash shape |
| Media upload/storage | **stubbed by scope** | out of scope (issue #61); `uploaded` is a client attestation — no object store exists yet |
| Report assembly + canonical hash | real | algorithm documented in `report.go`; verification recomputes it |
| AI-suggested findings | not started | A21 follow-up, advisory-only boundary (ARCHITECTURE.md §6) |
| Domain events | real (wiring) | envelope + JetStream publisher from the platform; requires `MOTIVRA_NATS_URL` to actually send |
| Tenant scoping | real | scope derived from verified JWT claims on every read; zero scope matches nothing (`1 = 0`) |
| PG-gated integration tests | real | `store_integration_test.go` via `backend/platform/pgtest`; runs when `TEST_DATABASE_URL` is set |

## Do NOT

- Never bypass `CanTransition` — no direct status writes in SQL, handlers
  or consumers.
- Never update or delete `inspection_findings`, `inspection_evidence`,
  `inspection_reports` or `inspection_session_transitions` rows; all four
  are append-only by database trigger, and corrections are new records.
- Never record findings or evidence on a session that is not IN_PROGRESS.
- Never assemble a report without evidence hashes — the readiness check
  and the database trigger both refuse.
- Never accept the inspector identity, tenant or captured-by from client
  input; they come from the verified token only.
- Never treat `uploaded` as proof of storage: until the storage adapter
  exists it is a client attestation, and the hash is the anchor.
