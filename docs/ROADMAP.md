# Motivra Roadmap

Waves sequence the build. Between waves, integration windows pause feature work for E2E, migration, security and load validation. Status lives here; progress is tracked in issues.

## Wave 0 — Discovery & architecture (DONE — PRs #8, #9)

**Goal:** the rules other waves follow.

- ADR-0001..0005: bounded contexts & repo layout; event schema & NATS topics; data ownership & migrations; security model; observability & SLOs
- Monorepo skeleton with ownership zones; CI quality gates; PR/issue templates
- Business glossary; issue taxonomy; definition of done
- **Exit criteria:** CI gates enforce on every PR; ADRs merged; templates live

## Wave 1 — Foundation (DONE — PRs #15, #16)

**Goal:** shared platform primitives.

- Platform foundation: config, HTTP server, middleware, pgx/sqlc data layer, Redis, NATS, Temporal wiring, OTel, health/readiness, graceful shutdown
- Identity & security: AuthN/AuthZ, RBAC (9 roles), sessions, device management, tenant isolation, audit foundations
- Observability backbone: metrics, logs, traces, dashboards, alerts
- **Exit criteria:** a service template spawns a production-shaped service in <1 day; identity issues tokens that every context accepts

## Wave 2 — Vehicle foundation (CORE MERGED — PR #17; tenant-scoping hardening next)

- Vehicle registry (canonical identity, VIN resolution), components, mileage
- Append-only service history; Vehicle Passport read model
- **Exit criteria:** passport assembles from history without mutation; provenance recorded

## Wave 3 — Inspection

- Dynamic inspection templates; offline inspection capture; media evidence (S3)
- Findings with severity (GREEN/AMBER/RED); verified report generation
- Pre-purchase inspection flow (Motivra Inspect wedge)
- **Exit criteria:** inspector completes an inspection offline; report verifies against evidence

## Wave 4 — Mobile garage (BACKEND CORE MERGED — PRs #21, #20; client apps next)

- Jobs & service request engine (full state machine, extensive tests)
- Dispatch engine with explainable scoring (fit, distance, equipment, inventory, performance)
- Technician app (React Native, offline-first): jobs, navigation, estimates, evidence, completion
- Customer web (Next.js): request → live status → approval → history
- **Exit criteria:** E2E happy path passes: request → dispatch → inspect → estimate → approve → repair

## Wave 5 — Money

- Payment provider abstraction; M-Pesa first, card/bank/wallet behind it
- Ledger in integer minor units; idempotency, webhooks, reconciliation, retries, refunds
- Warranties on completed work
- **Exit criteria:** payment success/failure/reconciliation all observable; no floating-point money anywhere

## Wave 6 — Network

- Parts platform: catalog, compatibility, suppliers, inventory, reservations, supplier orders
- Garage network: onboarding, verification, capacity, appointments; mobile → garage escalation
- Towing & roadside provider abstraction
- **Exit criteria:** escalation path mobile repair → garage → towing works with events end to end

## Wave 7 — B2B

- Fleet platform: depots, drivers, schedules, costs, downtime analytics (recurring revenue engine)
- Dealer platform: verified inventory, reconditioning
- Developer platform: API keys, OAuth apps, webhooks, sandbox, usage metering
- **Exit criteria:** a fleet admin manages 100 vehicles with per-vehicle economics; partner API v1 published

## Wave 8 — Intelligence

- Predictive maintenance, inspection intelligence, pricing and demand forecasting, dispatch optimization
- Vehicle risk engine & valuation support (passport-powered)
- **Exit criteria:** every AI output carries prediction/confidence/source/verification; AI stays advisory

## Wave 9 — Hardening

- Security audit vs the model in ARCHITECTURE.md §5; SAST/DAST/chaos testing
- Load testing on hot paths (service creation, dispatch, location updates)
- **Exit criteria:** SLOs measured and met on staging at production-shaped load

## Wave 10 — Production

- GitOps deployment (Argo CD), canary + rollback drills, runbooks, incident process
- **Exit criteria:** the first real customer job completes the full loop in production — request, dispatch, repair, approval, payment, recorded lifecycle
