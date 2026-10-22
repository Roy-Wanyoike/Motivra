# ADR-0008: Degraded Boot — Serve Liveness Before Dependencies

- **Status:** Accepted
- **Date:** 2026-10-22
- **Deciders:** Principal Platform Engineering (platform-02), per issue #122
- **Related:** ADR-0001 (platform layout — boot coordinator lives in `backend/platform`), ADR-0005 (observability — liveness/readiness semantics), ADR-0007 (verifier key sources — a production `config` blocker), issue #81 (readiness info-leak hardening), issue #122 (the production incident that forced this decision)

## Context

Every Motivra service binary exited at boot (`os.Exit(1)`) when its
configuration was incomplete or its infrastructure was unreachable:
`MOTIVRA_DATABASE_URL` missing, Postgres down, migrations failing, NATS
configured but unreachable, OTel setup failing. The posture was defensible
on a VM or under compose healthchecks — compose restarts the container
until Postgres is up, and a process that cannot do its job should say so
loudly.

It is wrong on Vercel Services mode. The backend deploys there as a
container service behind the root `vercel.json` rewrites, and Vercel's
container runtime has no knowledge of our dependencies: when the process
exits at boot, the runtime has nothing to route to and answers every
routed path — **including `/healthz`, which is specified as
dependency-free liveness** — with an opaque `500
FUNCTION_INVOCATION_FAILED`. The #122 production incident was exactly
this: the routing fix (PR #127) delivered requests to the container, the
container killed itself because the Vercel project had no
`MOTIVRA_DATABASE_URL`, and operators saw a bare 500 with zero
diagnostic signal. "The service cannot start" and "the service is
misconfigured" became indistinguishable from "the edge is broken".

## Decision

**A Motivra service process must always bind its listener and serve.**
Configuration and dependency failures surface as data on `/readyz` and as
`503 problem+json` on domain routes — never as process exit. Concretely:

- **Listen first, wire second.** The HTTP server starts immediately after
  port resolution. `/healthz` answers `200` unconditionally (it is
  liveness: "the process is up", not "the world is complete").
- **Wiring is a background loop.** `platform.Boot` runs ordered,
  idempotent, panic-isolated `BootStep`s (config validation, Postgres,
  migrations, NATS, JWKS warm-up…) with exponential backoff
  (250 ms → 10 s cap). A failed step records a named **blocker**; a
  recovered step clears it; readiness flips only when no blocker is
  latched.
- **Configuration problems are permanent blockers.** The process
  environment is fixed for the lifetime of the process, so a `config`
  blocker is never retried and never clears without a redeploy. The
  domain-route gate stays 503 — readiness must never lie to make a
  dashboard green.
- **Domain routes gate on readiness.** Until every step is green, every
  domain path answers `503` `application/problem+json` with
  `code: not_ready` and the sorted list of failed check NAMES. There is
  no stale serving, no partial API, no auth bypass.
- **Observability degrades, never blocks.** OTel/metrics setup failures
  record a named *degradation* (serving with reduced capability) instead
  of a blocker — an unreachable trace collector must not convert an
  observability outage into a product outage. `/readyz` reports the
  degraded capability names with an honest `200`.
- **Info-leak posture (issue #81, #122 security review).** Unauthenticated
  `/readyz` bodies carry check NAMES only. Full reasons travel to the
  service log (`boot_blocker` lines) and to the `reasons` field of
  `/readyz` responses for bearers with operator claims (ADMIN /
  SUPER_ADMIN, identity RBAC matrix).
- **Shutdown completeness.** Resources acquired by the (late-completing)
  wiring loop register shutdown hooks on the `Boot` coordinator, so
  graceful shutdown releases everything even when readiness never
  arrived.

Scope: identity, vehicles, jobs, dispatch — the four binaries deployable
in the container topology (`ARG SERVICE`). `payments` and `inspections`
mains keep fail-fast until they join the container topology; their boot
contract change is mechanical (same `platform.Boot` helper) and tracked
for the wave that deploys them there.

## Alternatives considered

1. **Owner-only fix (env vars on the Vercel project, keep fail-fast).**
   Necessary but insufficient: it fixes THIS project today, and the next
   misconfigured environment re-creates an opaque 500 with no
   diagnostics. Also leaves `/healthz` — the probe every uptime checker
   and the RUNBOOK §6 contract relies on — hostage to dependency state.
2. **Retry-then-exit at boot (bounded reconnection before giving up).**
   Preserves the exit contract but re-creates the same opaque failure
   after the retry budget expires, and still makes `/healthz` unavailable
   during the window when operators most need signal.
3. **Crash-loop the container and let the platform restart it.** On
   Vercel there is no supervisor to restart a container between
   invocations; every request during the crash-loop is a 500, and the
   restart storm burns Hobby-plan function quotas.

## Consequences

- **Positive:** `/healthz` is a truthful liveness signal on every
  platform; misconfiguration is self-describing (`readyz` names, logs,
  operator reasons); dependency outages degrade instead of dying; the
  zero-demo go-live gate and uptime probes keep operating during
  dependency incidents; Vercel deployments become diagnosable from the
  outside (503 with names vs 500 with nothing).
- **Negative:** a misconfigured deployment now serves (503s) instead of
  failing visibly at boot — mitigated by `boot_blocker` error logs,
  permanent `config` blockers that never flip readiness, and the RUNBOOK
  §6 post-deploy probes that include `/readyz` = 200.
- **Operational:** the §10 troubleshooting rows change meaning — "service
  exits on missing env" is now "container serves 503 not_ready with
  `failed:["config"]`". Compose healthchecks that gated on `/readyz`
  keep working unchanged (readiness semantics preserved).

## Verification

- `backend/platform/boot_test.go`: blocker latch/clear, permanent-blocker
  no-retry, backoff pacing, panic isolation, readiness flip only at zero
  blockers, degradation non-gating, shutdown-hook ordering.
- `backend/platform/server_test.go`: healthz dependency-free; readyz
  names-only for anonymous, `reasons` for operator claims; domain-gate
  503 before wiring and real router after; degradation honest-200.
- All four reworked mains boot-degrade in the Vercel topology (no
  `os.Exit` outside `cfg.Load` impossible- states), gated by the local
  test suite (`go build ./...`, `go vet ./...`, `go test ./...` — CI
  remains billing-locked per issue #10).
