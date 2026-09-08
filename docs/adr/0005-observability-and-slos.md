# ADR-0005: Observability Baseline and Initial SLOs

- **Status:** Accepted
- **Date:** 2026-09-08
- **Deciders:** Principal Architect (arch-01), per issue #2
- **Related:** ADR-0001 (platform package), ADR-0002 (correlation IDs on events), ADR-0004 (audit signals), `docs/ARCHITECTURE.md` §8

## Context

Motivra's core flows are distributed across bounded contexts communicating through events (ADR-0002) and durable workflows (Temporal). A single customer job traverses jobs → dispatch → inspections → estimates → payments → notifications. When something goes wrong in that chain, debugging without correlated traces and structured logs means reproducing field conditions that cannot be reproduced — a technician offline in Thika, an M-Pesa callback that arrived twice, a dispatch decision that took four seconds.

Operational reality at 0→1: no dedicated SRE function in the first weeks, one staging environment and (soon) one production environment, cost sensitivity, and a mobile field workforce whose connectivity failures must be distinguishable from backend failures. The observability decision must therefore be made now, so that:

1. Every service ships with the same telemetry wiring by construction (not per-team taste).
2. The correlation IDs that tie the event chain together (ADR-0002) are the same IDs in logs and traces.
3. Reliability conversations are anchored to SLOs from the first production incident, not retrofitted after one.

## Decision

### Telemetry stack

- **Traces and metrics: OpenTelemetry from day one.** Every service initializes the OTel SDK from the shared platform package, exports via **OTLP** to the collector, and is **env-gated**: telemetry is enabled by default in staging/production, off (or sampling to zero) in local unit-test contexts, sample-on in local compose. Trace context propagates across HTTP (W3C `traceparent`), into Temporal workflow headers, and onto NATS event headers — the `correlation_id` of ADR-0002 is carried alongside, so an event's producing trace is one hop from the consuming trace.
- **Logs: Go `slog`, structured JSON only.** No ad-hoc `fmt.Println`, no plaintext formats, no logging library proliferation. Every log line carries: timestamp, level, service name, **request ID, tenant ID, trace ID** (plus span ID where in-span), and message; domain fields ride in structured attributes, never string-interpolated. Request ID originates at the edge middleware; tenant ID comes from the validated token (ADR-0004); trace ID comes from the active span.
- **Metrics: Prometheus exposition.** Every service exposes `/metrics` (scraped by Prometheus), with the platform package providing standard RED instrumentation per route (rate, errors, duration histogram) and per-consumer lag/error counters for NATS consumers. SLO-specific counters (job creation attempts/successes, payment outcomes, notification delivery outcomes) are part of each context's metric contract, registered by the platform helper so naming stays consistent.
- **Signal storage:** Prometheus (metrics), Loki (logs), Tempo (traces), Grafana (dashboards) — matching the README stack. Collectors and storage are deployed with the platform (Wave 1 backbone); `/observability/` holds dashboards-as-code, alert rules, SLO definitions and runbooks.

### Platform wiring

`backend/platform` ships an opinionated `server.Run(...)` construct that wires, in one place: config loading, OTel tracer/meter providers with the OTLP exporter, slog logger factory with correlation fields, chi middleware chain (recover, request ID, tenant extraction, tracing, metrics), `/healthz` (liveness) and `/readyz` (readiness, checking DB/NATS/Temporal reachability), and graceful shutdown. **Every service inherits this by construction**: a `cmd/<service>/main.go` that skips it cannot produce consistent telemetry, and review rejects such bypasses. Health and readiness endpoints are unauthenticated but rate-limited and excluded from business metrics to avoid self-DoS.

### Initial SLOs

| SLO | Target | Notes |
|---|---|---|
| API availability | 99.9% | Measured at the edge for authenticated API routes, 30-day window |
| Job creation success | ≥ 99.5% | Valid `POST` service requests that reach `CREATED` state |
| Dispatch decision latency | p95 < 1.5 s | Proposal produced by the dispatch engine (excludes technician response time) |
| Payment success | ≥ 98% | Completed payments / initiated, provider-dependent; measured per provider and per attempt-class |
| Notification delivery | ≥ 99% | Delivered (provider-confirmed) / accepted, per channel |
| Workflow completion | ≥ 99.5% | Temporal workflows reaching completion vs. started, excluding user-abandoned |

Rules: SLOs are defined as code in `/observability/` (error budgets, burn-rate alerts); they are reviewed at each wave's integration window and tightened only with data, never loosened silently. Targets are initial and honest about external dependence (payment success is bounded by provider reliability in the operating markets; the SLO is on *our* handling of provider outcomes, not on the provider).

### Alert philosophy

- **Page on SLO burn** (fast-burn and slow-burn multi-window alerts on error budgets), plus page-worthy conditions that are SLO-independent: readiness failing fleet-wide, payment webhook processing stopped, JetStream consumer lag trending to unbounded.
- **Ticket on noise**: saturation warnings, slow single routes, certificate expiries, dependency degradations with designed fallbacks engaged (per `ARCHITECTURE.md` §7). A page that is not actionable is a defect in the alert; every alert links its runbook.
- **Dashboards per service plus one golden-signals overview** (traffic, errors, latency, saturation — fleet-wide) — the overview is the first screen in an incident; service dashboards are the second.

## Alternatives considered

**Vendor APM first** (Datadog/New Relic-style all-in-one). Rejected: subscription cost at 0→1 scale is significant in absolute terms and paid for the wrong asset (lock-in); the OTLP decision keeps every exporter swappable later — if a vendor is ever adopted, it consumes the same telemetry rather than replacing instrumentation. Also, vendors' proprietary agents conflict with the "wiring by construction" platform approach more than they help it.

**Logs-only at 0→1** ("we'll add tracing when it hurts"). Rejected: for an event-driven, multi-context flow, logs without traces cannot answer "where did this job's approval hang" without heroic grep sessions; retrofitting correlation into running systems is far more expensive than carrying IDs from day one.

**Hand-rolled metrics/logging without OTel.** Rejected: Prom-client-only instrumentation is fine for metrics but leaves tracing ad hoc; OTel's cost is a small SDK dependency, its benefit is a uniform semantic convention set (HTTP, DB, messaging) that makes service dashboards comparable.

**SLOs deferred until "production".** Rejected: SLOs are cheapest to establish before there is traffic to argue about, and Wave 9's exit criterion ("SLOs measured and met on staging at production-shaped load") requires the measurement machinery and target set to exist long before.

## Consequences

**Positive**

- Every service ships observable by construction: same logger, same tracer, same metric naming, same health contract — the Wave 1 exit criterion "service template spawns a production-shaped service in <1 day" is satisfied structurally.
- `correlation_id` consistency across events, logs and traces collapses cross-context debugging from archaeology to a single query.
- SLO-based paging bounds the on-call surface: pages map to user-visible harm, and error budgets arbitrate reliability-vs-velocity arguments with data.
- Vendor optionality: OTLP keeps the exit cost from self-hosted observability near zero.

**Negative / costs**

- The platform package grows one more responsibility (telemetry wiring) and must keep it dependency-light and stable; platform churn propagates to all services.
- Structured-logging discipline requires review vigilance (no PII — customer names, phone numbers, exact locations — in log attributes; redaction helpers ship in platform).
- Self-hosted Grafana/Loki/Tempo/Tempo storage is an operational load the team takes on in exchange for cost control; capacity for trace/log retention must be sized and revisited each wave.
- Initial SLO values are estimates; error-budget alerts will need tuning after the first weeks of real traffic, and payment/notification SLOs depend on provider data quality.
