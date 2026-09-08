# Motivra Architecture Overview

Status: **Wave 0 draft** — this document is the reference the ADRs refine. When this document and an ADR disagree, the ADR wins and this document gets updated.

## 1. What the system is

Motivra is a vehicle intelligence and service infrastructure platform: a set of bounded contexts that together record **vehicle identity, history and evidence**, and orchestrate a **distributed service network** (technicians, garages, parts, towing) against it. The mobile mechanic is the entry wedge; the Vehicle Passport is the compounding asset.

## 2. Bounded contexts and data ownership

Each context owns its tables, events and workflows. Cross-context access happens only through APIs, commands and versioned events — never by joining another context's tables.

| Context | Owns (non-exhaustive) | Emits (examples) |
|---|---|---|
| Identity | `users`, `roles`, `permissions`, `sessions`, devices | `identity.user.created.v1`, `identity.role.assigned.v1` |
| Customer & Organization | customers, organizations, addresses, preferences | `customer.created.v1`, `organization.created.v1` |
| Vehicles | `vehicles`, `vehicle_components`, `vehicle_passports`, `vehicle_service_history` (append-only) | `vehicle.created.v1`, `vehicle.mileage.recorded.v1`, `vehicle.history.updated.v1` |
| Service Catalog | categories, services, labour definitions, pricing, warranty definitions | `catalog.service.published.v1` |
| Jobs | `service_requests`, `jobs`, `job_assignments`; job state machine | `job.created.v1`, `job.assigned.v1`, `job.completed.v1` |
| Dispatch | technician availability, service areas, dispatch decisions & scores | `dispatch.proposed.v1`, `dispatch.accepted.v1` |
| Inspections | templates, items, measurements, media, findings, severity | `inspection.completed.v1`, `diagnosis.completed.v1` |
| Estimates | `estimates`, `estimate_items`; approval state machine | `estimate.created.v1`, `estimate.approved.v1`, `estimate.rejected.v1` |
| Payments | `payments`, `payment_transactions`, `refunds`; the ledger | `payment.created.v1`, `payment.completed.v1`, `payment.failed.v1` |
| Parts | catalog, OEM/aftermarket numbers, suppliers, `inventory`, reservations | `part.reserved.v1`, `supplier.order.placed.v1` |
| Garages | onboarding, verification, capacity, appointments | `garage.verified.v1`, `appointment.booked.v1` |
| Notifications | templates, preferences, delivery records | `notification.delivered.v1` |
| Intelligence (AI) | predictions, embeddings, model metadata — **advisory only** | `ai.prediction.produced.v1` |
| Fleet | fleets, depots, drivers, schedules, cost/downtime analytics | `fleet.vehicle.enrolled.v1` |
| Warranty | `warranties`, claims | `warranty.created.v1`, `warranty.claim.opened.v1` |

### Job state machine (Jobs context)

```text
CREATED → TRIAGING → DISPATCHING → ASSIGNED → ACCEPTED → EN_ROUTE → ARRIVED
        → INSPECTION → DIAGNOSIS → ESTIMATE → AWAITING_APPROVAL → APPROVED
        → REPAIRING → VERIFICATION → COMPLETED
Terminal/exception: CANCELLED, FAILED, ESCALATED
```

No repair that requires approval may begin without a valid approved estimate (Estimates context enforces this; Jobs and Payments consume it).

## 3. Event envelope

Every domain event on NATS JetStream carries, at minimum:

```json
{
  "event_id": "uuid",
  "event_type": "vehicle.service.completed.v1",
  "schema_version": 1,
  "aggregate_id": "vehicle:123",
  "tenant_id": "org:456",
  "actor_id": "user:789",
  "timestamp": "2026-09-08T09:00:15Z",
  "correlation_id": "uuid",
  "causation_id": "uuid | null",
  "payload": {}
}
```

Rules: versioned names (`*.v1`), idempotently consumable, consumers dedupe on `event_id`, producers own schemas.

## 4. Workflows

Temporal owns all long-running orchestration: dispatch, service jobs, payment orchestration, warranties, reminders, parts procurement, report generation. Only the owning domain modifies its workflow definitions; workflow changes require regression tests. Durable workflows are never reimplemented with ad-hoc goroutines or cron jobs.

## 5. Security model

- OIDC/OAuth2 authentication; server-side RBAC across roles: `CUSTOMER, TECHNICIAN, DISPATCHER, GARAGE, FLEET_ADMIN, SUPPORT, FINANCE, ADMIN, SUPER_ADMIN`.
- Tenant isolation and resource-level authorization enforced server-side.
- Never trusted from the client: prices, ownership, permissions, payment state, inspection state, vehicle history, technician status.
- Money in integer minor units; every financial operation carries idempotency key, transaction ID, audit record, provider reference, timestamp, currency. The ledger is authoritative; AI never modifies it.
- Webhooks: signature verification, provider event IDs, replay protection, reconciliation.
- CI gates: dependency scanning, container scanning, SAST; secrets never in the repo.

## 6. AI boundaries

AI outputs always carry `prediction`, `confidence`, `source`, `verification state`. AI is advisory: it proposes possible causes and recommended inspections; it never authorizes work, never mutates vehicle history, never overwrites a technician-confirmed diagnosis, and is never a financial source of truth.

## 7. Reliability & failure philosophy

External dependencies fail by design, not by incident: payment provider down → retry → alternate provider → reconcile; maps degraded → cached location → approximate routing; notifications degraded → queue → fallback channel; parts supplier down → alternate supplier. The technician app is offline-first (local DB, outbox, sync queue, idempotency, conflict detection, resumable uploads) — field technicians never lose job data to network failure.

Deployment: health/readiness probes, rolling and canary deploys, automated rollback, DB backups with point-in-time recovery. We build for high availability and graceful degradation; we do not claim absolute zero downtime.

## 8. Observability & SLOs

OpenTelemetry everywhere; Prometheus/Grafana/Loki/Tempo. Every service exposes health, readiness, metrics, traces, structured logs. Initial SLO candidates: API availability, job creation success, dispatch latency, payment success rate, notification delivery, workflow completion, mobile sync success.

## 9. Migrations

Migration chains are partitioned per domain (`/backend/migrations/<domain>`). Backward-compatible by default, tested, documented, with rollback strategies. Destructive changes follow expand → migrate → contract.
