# ADR-0002: Event Schema and NATS JetStream Topic Design

- **Status:** Accepted
- **Date:** 2026-09-08
- **Deciders:** Principal Architect (arch-01), per issue #2
- **Related:** ADR-0001 (bounded contexts), ADR-0003 (data ownership), `docs/ARCHITECTURE.md` §3

## Context

Motivra's core value loop — request → dispatch → inspect → estimate → approve → repair → pay → record — crosses at least seven bounded contexts. The integration style between them is asynchronous by default: the Jobs context does not call Payments synchronously to learn a payment succeeded; it consumes `payment.completed.v1`. Every context's correctness therefore depends on a shared, versioned, idempotently consumable event discipline.

Requirements the design must satisfy:

1. **Producers own schemas** (engineering principle 1); consumers integrate against contracts, not against whatever a producer happened to emit.
2. **At-least-once delivery is a fact of life** on any durable log; consumers must be safe to redeliver.
3. **Replayability**: new consumers (analytics, AI, the Vehicle Passport read model) must be able to start from history, not only from "now".
4. **Traceability**: a single job touches many events; debugging requires correlation fields on every message.
5. **Operations at 0→1**: a small team in Nairobi must run the broker alongside everything else, with backups, and without a dedicated platform-operations function on day one.
6. **Multi-tenancy**: events carry tenant context so per-tenant routing, filtering and isolation are possible without payload inspection.

`docs/ARCHITECTURE.md` §3 already sketches the envelope; this ADR fixes it as normative and adds the topic naming, delivery and dedupe rules.

## Decision

**NATS JetStream is the event backbone.** All domain events are published to JetStream streams; services consume via durable push/pull consumers. The envelope and topic scheme below are normative for every context.

### Event envelope

Every event, exactly as `docs/ARCHITECTURE.md` §3:

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
  "causation_id": "uuid",
  "payload": {}
}
```

Field rules:

- `event_id` — UUIDv4 (later v7 if time-ordering proves necessary), the dedupe key. Immutable.
- `event_type` — `<domain>.<aggregate>.<event>.v<n>`; must equal the NATS subject by convention, so a subject is self-describing.
- `schema_version` — integer, bumps when the *payload* shape changes in a compatible way; the `.v<n>` suffix bumps when the change is breaking.
- `aggregate_id` — `<type>:<id>` of the aggregate the event is about.
- `tenant_id` — tenant scope (`org:<id>`); required on every event; system-level events use a reserved system tenant.
- `actor_id` — the user or service principal that caused the event (`system` for automated workflows).
- `timestamp` — RFC3339, UTC, producer clock; not used for ordering (JetStream sequence is).
- `correlation_id` — stable across an entire workflow (a job's lifetime); propagated from the originating request.
- `causation_id` — the `event_id` (or request ID) that directly caused this event; null for initiators.
- `payload` — versioned JSON; no envelope fields may be duplicated inside it.

### Topic naming

Subjects follow `motivra.<domain>.<aggregate>.<event>.v<n>`:

- `motivra.vehicles.vehicle.created.v1`
- `motivra.jobs.job.assigned.v1`
- `motivra.payments.payment.completed.v1`
- `motivra.dispatch.proposal.accepted.v1`

Rules:

- Domain and aggregate segments are singular, lowercase, snake_case.
- Breaking event changes create a new version (`...v2`) on a new subject; the old subject remains consumable for an agreed compatibility window before its consumer is retired.
- **One JetStream stream per domain** (e.g. `MOTIVRA_VEHICLES` bound to `motivra.vehicles.>`) so retention, limits and storage are tuned per domain and stream config changes stay within one owner's zone.
- Consumers use durable, explicit-ack consumers; no fire-and-forget consumption anywhere.

### Idempotent consumption

Delivery is **at-least-once**; every consumer MUST be idempotent. The standard mechanism:

- Consumers dedupe on `event_id` via a **dedupe store abstraction** in `backend/platform` (interface: record/exists within retention window). The first implementation is PostgreSQL (a `processed_events` table keyed on `event_id` with the consumer name); Redis is permitted as a fast-path cache in front of it but never as the sole dedupe record.
- Side effects and dedupe-marking happen in one transaction where the consumer has a database (the common case), so a crash between "apply" and "ack" cannot double-apply.
- Handlers that cannot transactionally dedupe (e.g. pure notification senders) use idempotency keys handed to the downstream provider where supported, and accept the residual duplication cost.

### Schema ownership

- Producers own schemas in `/contracts/events/<domain>/` (JSON Schema per event, one file per event type/version).
- The event registry document `/contracts/events/README.md` lists every event: name, version, producer, stream, known consumers, schema path. A stub lands with the first producing context (Wave 1–2); it is required before any consumer integrates.
- Producers may add optional payload fields within the same `schema_version`; removing or renaming fields requires a version bump and the compatibility window.
- Consumers must not assume fields beyond the published schema. Unknown payload fields are ignored, never rejected.

## Alternatives considered

**Kafka.** The industry default for event streaming, with a rich ecosystem. Rejected at this stage: operational weight (ZooKeeper/KRaft topology, partition rebalancing, JVM tuning, per-broker sizing) is a poor fit for a small team shipping its first loop; the volume profile of a 0→1 marketplace (jobs, payments, notifications) is far below what justifies Kafka's partition machinery. NATS JetStream gives durable streams, replay, consumer offsets and subject wildcards with a single small static binary that runs in Docker Compose locally and Kubernetes in production. Revisit trigger: sustained throughput or consumer-fanout requirements (e.g. high-frequency telemetry ingestion) that outgrow JetStream, at which point the envelope and topic scheme port with minimal change.

**RabbitMQ.** Strong routing and low latency, familiar operations. Rejected: its persistence model is queue-oriented (message discarded on ack) rather than log-oriented (stream retained for replay). Losing replay sacrifices new-consumer-from-history, projection rebuilds for analytics/AI, and audit-friendly reprocessing — all load-bearing for the Vehicle Passport and Intelligence road. AMQP's dynamic topology also invites ad-hoc routing logic instead of the disciplined subject scheme above.

**Direct HTTP/synchronous integration between services.** Simpler to reason about initially. Rejected as the default integration style: it couples availability (a payments outage becomes a jobs outage), forfeits replay, and pushes every cross-context question into request/response chatty patterns. Synchronous calls remain appropriate for command-style interactions with a hard same-request answer (e.g. authorization checks); events remain the default for facts.

**Cloud-managed event bus** (e.g. managed Kafka/SNS+SQS). Rejected for now: cost at low volume, provider lock-in before the provider-abstraction patterns exist, and a stack the local/offline-first development story cannot reproduce faithfully. The README's provider-abstraction principle keeps this door open.

## Consequences

**Positive**

- Every event is self-describing, tenant-tagged and traceable end to end (`correlation_id` links a job's whole event chain; ADR-0005 logs carry the same ID).
- Per-domain streams keep JetStream configuration inside ownership zones — a dispatch engineer never touches the vehicles stream.
- The dedupe abstraction makes "write an idempotent consumer" a platform primitive, not per-team folklore.
- Replay-friendly log semantics mean the Vehicle Passport read model, analytics projections and AI training sets are all just consumers, not special cases.

**Negative / costs**

- Versioned subjects require per-domain stream configuration; adding a domain is a deliberate act (stream + subject + schema + registry entry), which is the point but is friction.
- Every consumer carries dedupe state and its storage cost; retention windows must be tuned per stream and monitored (ADR-0005 metrics include consumer lag).
- The envelope is a public contract from day one — changes to it are breaking changes and need an ADR-level decision.
- Producers must ship schemas before consumers integrate, which adds a small lead time to every cross-context feature; the registry document is the enforcement point.
