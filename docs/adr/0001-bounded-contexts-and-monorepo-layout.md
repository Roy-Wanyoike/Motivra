# ADR-0001: Bounded Contexts and Monorepo Layout

- **Status:** Accepted
- **Date:** 2026-09-08
- **Deciders:** Principal Architect (arch-01), per issue #2
- **Related:** ADR-0002 (events), ADR-0003 (data ownership), `docs/ARCHITECTURE.md` §2

## Context

Motivra is a multi-surface platform: nine product surfaces (Drive, Tech, Inspect, Garage, Fleet, Dealer, Command, Intelligence, Developer) run on one shared backbone of vehicle identity, history, dispatch and money. The build is executed by a distributed, partially agent-based engineering organization working in parallel waves. Both facts create the same risk: without explicit, mechanically discoverable boundaries, parallel work collides — shared files conflict, one team's schema change breaks another team's service, and ownership becomes an oral tradition instead of a review gate.

The stack decisions are already made at the platform level (Go backend, PostgreSQL+PostGIS, NATS JetStream, Temporal, Next.js web, React Native mobile — see `README.md`). What is not yet decided:

1. How code is physically organized (one repo vs. many, one Go module vs. many).
2. How the domain model from `docs/ARCHITECTURE.md` §2 maps to directories and services.
3. How ownership of tables, events and workflows is made visible in the repository layout itself.

Constraints: the team is small at 0→1 but the domain model is wide (13+ backend contexts); CI must stay fast enough to gate every PR; agents and humans alike need file-level ownership rules that a reviewer (or a coordinator script) can check mechanically.

## Decision

**Adopt a modular monorepo with a single root Go module `github.com/Roy-Wanyoike/Motivra`.** Bounded contexts map one-to-one to top-level directories; the directory boundary *is* the ownership boundary.

### Repository layout

```text
/backend/                 # single Go module root: github.com/Roy-Wanyoike/Motivra
  platform/               # shared primitives: config, middleware, db, otel, health (see ADR-0005)
  identity/               # AuthN/AuthZ, RBAC, sessions, tenant isolation
  vehicles/               # registry, VIN resolution, passport, append-only history
  jobs/                   # service requests, job state machine, assignments
  dispatch/               # explainable dispatch scoring, availability, service areas
  inspections/            # templates, evidence, findings, verified reports
  estimates/              # itemized quotes, approval state machine
  payments/               # provider abstraction, M-Pesa, ledger, reconciliation
  parts/                  # catalog, compatibility, inventory, reservations
  garages/                # onboarding, verification, capacity, appointments
  notifications/          # templates, preferences, delivery (SMS/WhatsApp/push/email)
  ai/                     # Motivra Intelligence — advisory outputs only
  analytics/              # projection/consumer services feeding ClickHouse
  migrations/<domain>/    # per-domain SQL migration chains (ADR-0003)
  cmd/<service>/main.go   # one entrypoint per deployable service
/apps/                    # client applications
  customer/               # Motivra Drive (Next.js)
  technician/             # Motivra Tech (React Native, offline-first)
  admin/                  # Motivra Command (ops control plane)
  fleet/                  # Motivra Fleet portal
/contracts/               # OpenAPI, protobuf, event schemas — producers own these
/infrastructure/          # Docker, Helm, OpenTofu, Argo CD, environment config
/observability/           # dashboards, alerts, SLO definitions, runbooks
/docs/                    # architecture, ADRs, glossary, roadmap, engineering docs
```

### Ownership rules

1. **A bounded context owns its packages, its tables, its `/backend/migrations/<domain>/` chain, its event schemas, and its Temporal workflows.** Nothing else writes to them.
2. **Cross-context interaction is via APIs, commands, or versioned events only** (ADR-0002). No context imports another context's internal packages or queries another context's tables. `platform` is the only shared import surface, and it must stay dependency-free of domain code.
3. **Service entrypoints are thin.** `cmd/<service>/main.go` wires config, platform primitives and the context's package; all logic lives in the context package where it is testable.
4. **Contracts live centrally** in `/contracts` because they are the negotiation surface between contexts; the producing context is the sole author of its schema files (ADR-0002, ADR-0003 conventions apply).
5. **Ownership is enforced by review plus CODEOWNERS-style conventions**, not by build magic. Each PR declares its ownership zone; reviewers reject cross-zone edits that arrived without a dependency request on the owning issue.
6. **CI builds and tests all contexts** on every PR. The monorepo accepts the cost of full validation in exchange for atomic cross-cutting changes (e.g. a contract change plus both consumer updates in one reviewable PR).

### What a "service" is

A deployable unit is `cmd/<service>`. In Wave 1 most contexts deploy as one service per context; the layout permits splitting a context's internals into multiple `cmd/` binaries later (e.g. a dispatch-decision worker) without touching other contexts. Splitting *contexts* into separate repos/modules is out of scope until the revisit trigger below fires.

## Alternatives considered

**Per-service Go modules / polyrepo.** Each context gets its own module (`github.com/Roy-Wanyoike/Motivra/identity` as `go.mod` root) or its own repository. Rejected for this stage: duplicate tooling and CI plumbing multiplied by 13+ contexts is the dominant cost at 0→1; cross-context contract changes become multi-PR dances with ordering hazards; shared platform code needs a publish/consume cycle. Revisit triggers: more than ~10 independently deployable services with separate teams, CI wall-time on full builds becoming a bottleneck for everyone, or diverging release cadences. The directory mapping survives that split — a context can be lifted out because it never reached into its neighbours.

**Single-package application.** One `/backend` package tree organized by technical layer (`/handlers`, `/models`, `/repos`). Rejected: boundary erosion is guaranteed. In a layer-organized codebase, any file can import any other file, domain invariants (append-only history, ledger authority, tenant scoping) cannot be located, and agent/human ownership cannot be assigned per directory. The costs show up as review latency and defect escapes rather than as a one-time design expense.

**Microservices from day one.** A service per bounded context with independent deployment pipelines from the start. Rejected as premature: operational surface (13 pipelines, 13 dashboards, distributed tracing required just to debug a happy path) outruns the team's ability to operate it, while the domain is still moving. The modular monorepo keeps the extraction option open — bounded contexts are the unit of future extraction either way.

## Consequences

**Positive**

- The ownership zone of every agent and engineer is a directory path — mechanically checkable in review, and the basis for the agent registry (`docs/engineering/agents.md`).
- Cross-context changes are visible and reviewable in one PR when they must be atomic.
- `platform` becomes the enforced choke point for cross-cutting concerns (middleware, JWT validation, OTel wiring), so every service inherits them by construction (ADR-0004, ADR-0005).
- New contexts follow a fixed recipe: directory, migration chain, `cmd/<service>`, contracts — the service-template path Wave 1 requires.

**Negative / costs**

- CI builds and tests everything on every PR; per-context affected-path optimization is deferred work, not cancelled work.
- The single module means a careless `import` can cross a boundary silently; reviewers and lint conventions must catch what the compiler does not. A import-boundary lint is expected in Wave 1 platform work.
- Monorepo-scale hygiene (branch discipline, one owner per branch) is a coordination requirement, handled through the agent registry and conflict protocol rather than tooling.
- Splitting a context later requires untangling its shared-`platform` dependency graph; keeping `platform` small and stable is the mitigation.
