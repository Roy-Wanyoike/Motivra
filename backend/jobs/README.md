# Backend — Jobs

Service requests, the job state machine (18 statuses, CREATED through
COMPLETED plus CANCELLED/FAILED/ESCALATED) and technician assignments.
See docs/ARCHITECTURE.md §2 for the context boundaries.

## Job state machine

Every status move passes `CanTransition(from, to)`; the state machine is
the single source of truth (`statemachine.go`) and nothing may write a job
status without going through it. Terminal states are immutable.

```
CREATED ──▶ TRIAGING ──▶ DISPATCHING ──▶ ASSIGNED ──▶ ACCEPTED ──▶ EN_ROUTE
   │            │             │  ▲          │             │
   │            │             │  └──────────┘ (reassign)  │ (release back)
   │            │             ▼                            ▼
   │            │         ESCALATED                    DISPATCHING
   │            ▼
   │        CANCELLED ...
```

Mainline (text form, `A -> B` means a legal move):

```
CREATED   -> TRIAGING | CANCELLED
TRIAGING  -> DISPATCHING | CANCELLED
DISPATCHING -> ASSIGNED | ESCALATED | CANCELLED
ASSIGNED  -> ACCEPTED | DISPATCHING (reassign) | CANCELLED
ACCEPTED  -> EN_ROUTE | DISPATCHING (release) | CANCELLED
EN_ROUTE  -> ARRIVED | CANCELLED
ARRIVED   -> INSPECTION | FAILED
INSPECTION -> DIAGNOSIS
DIAGNOSIS -> ESTIMATE
ESTIMATE  -> AWAITING_APPROVAL
AWAITING_APPROVAL -> APPROVED | ESCALATED | CANCELLED
APPROVED  -> REPAIRING
REPAIRING -> VERIFICATION | FAILED
VERIFICATION -> COMPLETED | REPAIRING (rework)
COMPLETED, CANCELLED, FAILED: terminal, immutable
ESCALATED: reachable dead end (not terminal)
```

### Guard rules

- No repair before approval: REPAIRING is reachable only from APPROVED,
  and APPROVED only from AWAITING_APPROVAL. There is no path from a
  diagnosis or estimate straight to REPAIRING.
- No assignment outside dispatching: only a DISPATCHING job takes a
  technician; an ASSIGNED job is reassigned by stepping back through
  DISPATCHING (ASSIGNED -> DISPATCHING -> ASSIGNED).
- Cancellation is possible only while the job has not reached physical
  work (CREATED through ACCEPTED, plus AWAITING_APPROVAL). Once a
  technician is ARRIVED the outcomes are COMPLETED or FAILED.
- ESCALATED has no outgoing edges: escalation is a hand-off decision, not
  a status to move through.

### Append-only transition log

`job_transitions` records every successful move (`from_status`,
`to_status`, `actor_id`, `reason`, `created_at`) in the same transaction
as the status update. Rows are never updated or deleted; the audit trail
is exposed read-only at `GET /v1/jobs/{id}/transitions`.

## HTTP API (v1)

| Method | Path | Who | Purpose |
|---|---|---|---|
| POST | /v1/requests | authenticated | create a service request |
| POST | /v1/requests/{id}/job | authenticated | convert a request into a job |
| GET | /v1/jobs?status=&technician_id=&limit=&offset | DISPATCHER/ADMIN/SUPER_ADMIN (unrestricted, audited) + GARAGE (own garage tenant only) | list jobs, optionally narrowed by status/technician (`status` omitted = every status, #123; garage bound = token `tenant_id` claim, #165) |
| GET | /v1/jobs/{id} | authenticated | fetch one job |
| POST | /v1/jobs/{id}/transitions | DISPATCHER/ADMIN/SUPER_ADMIN | move the job through the machine |
| GET | /v1/jobs/{id}/transitions | authenticated | read the audit trail |
| POST | /v1/jobs/{id}/assignments | DISPATCHER/ADMIN/SUPER_ADMIN | dispatch a technician |
| POST | /v1/jobs/{id}/accept | TECHNICIAN | accept the assignment |

Contract: `contracts/jobs/openapi.yaml`. Errors are problem+json via
`platform.WriteError` (401/403/404/409/422 mapped from the domain
sentinels `ErrConflict`, `ErrAlreadyConverted`, `ErrRequestNotFound`,
`ErrJobNotFound`).

### Garage-scoped listing (#165)

`GET /v1/jobs` additionally admits the `GARAGE` organizational principal
(the apps/garage console job board). Its scope comes exclusively from the
token's `tenant_id` claim — the garage's tenant id — and the query layer
(`jobScopePredicate` tenant branch, jobs migration 000006) restricts every
page to that tenant, so no cross-garage row is ever returned; there is no
client-supplied garage selector. A GARAGE token without a `tenant_id`
claim answers 403 (fail closed, the garages-domain convention, #170); a
malformed claim answers 401. Operators keep the unrestricted board, and
each operator page appends one `jobs.job.listed` record to the platform
`audit_log` (ADR-0004 privileged-read auditing; a failed record fails the
request). Garage and personal pages are tenant-bounded reads, not
privileged ones, and are not audited.

## Events (JetStream, domain `jobs`)

| Event | Emitted when |
|---|---|
| `request.received.v1` | a service request is created |
| `job.created.v1` | a request is converted into a job |
| `job.status.changed.v1` | any legal transition (payload carries from/to/reason/actor) |
| `job.assigned.v1` | a technician is dispatched to a job |

`cmd/jobs` constructs a `platform.NATSPublisher` when `MOTIVRA_NATS_URL` is
set (issue #28, deferral 2); while the variable is unset the service runs
with a nil publisher and `Service` skips publishing.

## Service wiring

`cmd/jobs/main.go`: config -> logger -> tracing/metrics -> Postgres pool
-> `platform.MigrateUp(jobsnextmigrations.FS, "schema_migrations_jobs")`
-> `jobs.NewPostgresStore` -> `jobs.NewService` -> server with
`platform.AuthMiddleware` -> `jobs.Routes` -> graceful shutdown.

## Do NOT

- Never bypass `CanTransition` — no direct status writes in SQL, handlers
  or consumers; the state machine is the only mutation path.
- Never update or delete `job_transitions` rows; the log is append-only.
- Never transition or assign from customer-held roles; role gates live in
  the route group (`platform.RequireRole`), but the machine enforces the
  real safety invariant regardless of caller.
- Never treat ESCALATED as terminal: it is a hand-off state for the
  dispatch queue to pick up.
