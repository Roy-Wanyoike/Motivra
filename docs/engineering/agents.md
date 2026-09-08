# Agent Registry

The operating roster for Motivra's coordinated multi-agent engineering organization. The build runs like a distributed team: every agent owns a zone, claims work through issues, and lands changes through reviewed PRs — the same rules as human contributors, enforced the same way.

## How this registry works

Each agent claims an issue by posting a claim comment on it (agent name, branch, and the exact files it declares). Declared file ownership is the ownership boundary: an agent never blind-edits shared surfaces (shared docs, `go.mod`, CI workflows, PR/issue templates) — cross-zone needs go through a dependency request on the owning issue, never a drive-by edit. Work follows a fixed status protocol: **PLANNED → CLAIMED → IN_PROGRESS → IN_REVIEW → MERGED**, with **BLOCKED** as the escape state when a dependency or coordination conflict stops progress (the agent records the blocker on its issue and in this registry). The "Role charter" below is the standing assignment of zones; "Active dispatches" is the live state of in-flight work. One owner per branch, one PR per issue, and the registry is updated by the owning agent as status changes.

## Role charter (A01–A30)

| Role | Ownership zone | Primary deliverables | Status |
|---|---|---|---|
| A01 Principal Architect | `docs/adr/`, `docs/engineering/`, `docs/ARCHITECTURE.md` | Foundational ADRs (0001–0005), agent registry, architecture overview maintenance, cross-context boundary rulings | PLANNED |
| A02 Product Manager | `docs/ROADMAP.md`, `docs/IDEAS.md`, issue taxonomy | Wave sequencing, issue definitions, prioritization, definition of done, metric targets | PLANNED |
| A03 Product Designer | `/apps` shared design system, UX specs in `docs/design/` | Motivra design system, product flows, accessibility standards, UI review bar | PLANNED |
| A04 Platform Foundation | `/backend/platform` | Config, HTTP server construct, middleware, pgx/sqlc data layer, Redis/NATS/Temporal wiring, health endpoints, service template | PLANNED |
| A05 Identity & Security | `/backend/identity` | AuthN (JWT + refresh sessions), RBAC matrix, tenant isolation, device management, audit foundations | PLANNED |
| A06 Vehicle Identity | `/backend/vehicles` (registry, VIN) | Canonical vehicle identity, VIN resolution, component registry | PLANNED |
| A07 Vehicle Registry & History | `/backend/vehicles` (history) | Append-only service history, mileage records, corrections-as-new-records semantics | PLANNED |
| A08 Vehicle Passport | `/backend/vehicles` (passport read model) | Passport assembly from history, provenance display, verified report surfacing | PLANNED |
| A09 Inspection Platform | `/backend/inspections` (templates, findings) | Dynamic inspection templates, severity model (GREEN/AMBER/RED), inspection state machine | PLANNED |
| A10 Evidence Platform | `/backend/inspections` (evidence, media) | Evidence capture, provenance records, S3 media handling, verified report generation | PLANNED |
| A11 Service Platform | `/backend/jobs` | Service requests, job state machine, assignments, escalation triggers | PLANNED |
| A12 Dispatch | `/backend/dispatch` | Explainable dispatch scoring (fit, distance, equipment, performance), availability, service areas | PLANNED |
| A13 Technician | `/backend/dispatch` (technician profiles, quality), technician domain events | Technician verification, skills, quality scoring, earnings surface | PLANNED |
| A14 Mobile/Offline | `/apps/technician` | Offline-first field app: local DB, outbox, sync queue, conflict resolution, resumable uploads | PLANNED |
| A15 Garage | `/backend/garages` | Garage onboarding, verification, capacity, appointments, mobile-to-garage escalation | PLANNED |
| A16 Parts | `/backend/parts` | Parts catalog, OEM/aftermarket compatibility, inventory, reservations, supplier orders | PLANNED |
| A17 Payments | `/backend/payments` | Provider abstraction, M-Pesa first, integer-minor-unit ledger, webhooks, reconciliation, refunds | PLANNED |
| A18 Warranty | `/backend/warranties` (planned context) | Warranties on completed work, claims workflow, warranty cost tracking | PLANNED |
| A19 Fleet | `/backend/fleet` (planned context), `/apps/fleet` | Depots, drivers, maintenance schedules, downtime analytics, per-vehicle economics | PLANNED |
| A20 Dealer | `/backend/dealers` (planned context) | Verified inventory, reconditioning workflows, buyer trust surfaces | PLANNED |
| A21 AI/ML | `/backend/ai` | AI triage, technician copilot, predictive maintenance — advisory outputs only (prediction, confidence, source, verification state) | PLANNED |
| A22 Data Engineering | `/backend/analytics`, ClickHouse projections | Event-consumption pipelines, analytics projections, unit-economics and investor metrics layers | PLANNED |
| A23 Integrations | `/contracts` integrations, country connectors | Payment/maps/SMS/WhatsApp provider adapters, country-connector layer, designed failure paths | PLANNED |
| A24 Web Platform | `/apps/customer`, shared web packages | Motivra Drive (Next.js), request → live status → approval → history flows, shared UI implementation | PLANNED |
| A25 API/Developer Platform | `/contracts`, developer platform service | Public API v1, API keys, OAuth apps, webhooks, sandbox, usage metering, SDKs | PLANNED |
| A26 Observability/SRE | `/observability`, deployment observability stack | Dashboards-as-code, alert rules, SLO definitions, runbooks, incident process | PLANNED |
| A27 Security Engineering | Cross-cutting review; security middleware in `/backend/platform` | Threat model, SAST/dependency/container scanning, secrets discipline, security review sign-off | PLANNED |
| A28 QA/Test Engineering | Test suites across `/backend`, `/apps`; E2E harness | Test strategy, unit/integration/contract/E2E coverage, offline-sync tests, failure-path tests | PLANNED |
| A29 DevEx | `scripts/`, `Makefile`, Docker Compose, service templates | Local dev environment (`make dev`), CI ergonomics, developer onboarding path | PLANNED |
| A30 Release Engineering | `/infrastructure`, `.github/workflows` | GitOps (Argo CD), Helm charts, canary + rollback automation, release pipeline | PLANNED |

Zones marked "planned context" follow the bounded-context recipe in ADR-0001 and are created by their owning agent with the context's first PR. Where two roles share a directory (e.g. A06/A07/A08 in `/backend/vehicles`), the sub-zone in parentheses is the split — they coordinate through dependency requests, not parallel edits to the same files.

## Active dispatches

| Agent | Role | Issue | Branch | Ownership (files) | Notes | PR | Status |
|---|---|---|---|---|---|---|---|
| arch-01 | Principal Architect (A01) | #2 | agent/architecture/adrs | docs/adr, docs/engineering, ARCHITECTURE.md index | ADR-0001..0005 + registry | #8 | MERGED |
| infra-01 | DevEx/Release (A30/A29) | #3 | agent/infra/skeleton-ci | go.mod, CI, compose, Makefile, stubs | shared dep pinning + quality gates | #9 | MERGED |
| platform-01 | Platform Foundation (A04) | #4 | agent/platform/foundation | backend/platform, cmd/template | coordinator-completed after 2 infra failures | #15 | MERGED |
| identity-02 | Identity & Security (A05) | #5 | agent/identity/foundation | backend/identity, migrations, cmd, contracts | part 1 + part 2 continuation; MinPasswordLength=10, HMAC refresh | #16 | MERGED |
| vehicles-03 | Vehicle Identity (A06-A08) | #6 | agent/vehicles/passport | backend/vehicles, migrations, cmd, contracts | part 1 recovered by coordinator; part 2 continuation | #17 | MERGED |
| jobs-01 | Jobs Engine (A11/A12) | #18 | agent/jobs/engine | backend/jobs, migrations, cmd, contracts | 18-status machine, ~350 subtests | #21 | MERGED |
| dispatch-01 | Dispatch Engine (A12) | #19 | agent/dispatch/scoring | backend/dispatch, cmd, contracts | explainable 6-factor scoring | #20 | MERGED |

### Integration Window 1 (completed)

After the Wave 4 backend-core merges, feature work paused for validation per the build directive: exhaustive state-machine matrices green, migration validator green across 10 domains, race-enabled test suite green locally, ownership-zone audits clean on every merged PR. Known deferred work is tracked in PR review notes (tenant scoping on reads, NATS publisher wiring, Postgres-gated integration tests in CI).

## Status protocol

## Conflict protocol

1. **Claim first.** Post a claim comment on the issue (agent name, branch, declared files) before writing any code or docs — unclaimed-then-touched files cause the conflict, not solve it.
2. **Declare files precisely.** The claim lists the exact paths the agent will touch; if the work grows beyond the declared set, update the claim comment before pushing.
3. **Check active dispatches.** Before starting, verify in this registry that no other agent holds an overlapping dispatch on your target paths; overlap means coordinate, not proceed.
4. **Dependency request for cross-zone needs.** Need a change outside your zone? Open a dependency request on the owning issue and wait for the owner's PR — never edit shared surfaces (`go.mod`, `.github/`, shared docs) unilaterally.
5. **Coordinator resolves.** Unresolved overlaps, contested ownership, or BLOCKED agents are escalated to the build coordinator, whose call is final and recorded on the affected issues.
