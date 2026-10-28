# ADR-0009: In-Process API Gateway — One Container Serves Every Domain

- **Status:** Accepted
- **Date:** 2026-10-28
- **Deciders:** Principal Platform Engineering (principal-arch-01), per issue #202
- **Related:** ADR-0001 (bounded contexts — the gateway composes domains, it never merges them), ADR-0003 (per-domain migration chains — the gateway applies all six), ADR-0004 (security baseline — auth/RBAC stay in the domain routers), ADR-0007 (asymmetric JWT — the gateway hosts the issuer AND the verifier posture), ADR-0008 (degraded boot — the gateway inherits the contract wholesale), issue #122 (the single-container topology this decision completes), issue #202 (the production defect that forced this decision)

## Context

The Vercel container topology runs ONE backend container (`services.app`,
entrypoint `Dockerfile.vercel`), and the root `vercel.json` rewrites route
ALL of `/v1/*` — plus `/healthz` and `/readyz` — to it. But the image builds
exactly one Go binary via `ARG SERVICE` (identity | vehicles | jobs |
dispatch; default `identity`, the value passed by the `app` service), so
production answered `/v1/*` from the **identity domain only**: every
vehicles, jobs, dispatch, payments and inspections route — merged, tested
and documented — was unreachable behind a 404. Discovered 2026-10-28 while
closing #195; blocks production acceptance of every backend-surface issue
(#165–#170, #185/#186).

Two architectural options existed for making the single container serve a
multi-domain API:

(a) **Gateway binary** — `cmd/gateway` imports every domain's router
    constructor and mounts it under `/v1` in one process: one container, no
    network hop, one PORT binding, one boot/degraded-boot contract.

(b) **Per-domain containers** — one container service per backend domain
    plus per-prefix rewrites in `vercel.json`: 5+ containers, one Vercel
    service each (Hobby-plan limits and quota burn apply), per-service env
    surfaces, N cold starts, N readiness postures.

## Decision

**Build `cmd/gateway` — the in-process API gateway — and make it the
default `SERVICE` of the container topology (ADR-0009, option (a)).**

Concretely:

- **The domains own their routing; the gateway only composes.** The gateway
  imports each domain's router constructor and calls it exactly as the
  domain's own `cmd/<domain>/main.go` does — `identity.Routes`,
  `vehicles.Routes`, `jobs.Routes`, `dispatch.Routes`, `payments.Routes`,
  `inspections.Routes`, `garages.Routes` — on one chi router. No routing
  logic is forked, no middleware is duplicated: the domain routers keep
  their own paths, their own RBAC gates (`RequireAuthenticated` /
  `RequireRole` wrappers inside each `Routes`) and their own error
  rendering. The gateway adds exactly two handlers of its own: problem+json
  404 for unknown `/v1` prefixes and problem+json 405 for wrong methods
  (after the wiring gate opens, the platform Server hands every non-health
  path to the domain router; a bare-text chi 404 would break the API's
  problem+json contract).
- **One shared dependency graph.** One PostgreSQL pool serves all domains
  (the cmd/jobs jobs+garages precedent), one NATS publisher, one token
  validator. All six per-domain migration chains are applied by the wiring
  loop (ADR-0003 chains stay separate). The database is a hard dependency
  of the whole process: identity, vehicles, jobs, garages, payments and
  inspections all own authoritative state. Dispatch scores statelessly and
  writes its decision records to the same pool when present.
- **The prefix map** (registered verbatim by the domain constructors):

  | Prefix | Domain | Routes constructor |
  | --- | --- | --- |
  | `/v1/auth/*`, `/v1/users/*`, `/v1/admin/*`, `/.well-known/jwks.json` | identity | `identity.Routes` |
  | `/v1/vehicles/*` | vehicles | `vehicles.Routes` |
  | `/v1/requests/*`, `/v1/jobs/*` | jobs | `jobs.Routes` |
  | `/v1/garage/capacity` | garages (rides the jobs process, #170) | `garages.Routes` |
  | `/v1/dispatch/*` | dispatch | `dispatch.Routes` |
  | `/v1/payments/*` | payments | `payments.Routes` |
  | `/v1/inspections/*` | inspections | `inspections.Routes` |

  (`/v1/identity/*` deliberately does not exist: the identity domain's
  documented paths are `/v1/auth/*` and `/v1/users/*` — the gateway keeps
  the documented surface; nothing is moved or removed.)
- **Boot semantics are inherited, not re-invented** (ADR-0008): listener
  binds first; `/healthz` is dependency-free liveness; `/readyz` reports
  machine-readable blocker NAMES; domain routes answer 503 problem+json
  until the wiring loop completes. The fail-fast `payments`/`inspections`
  mains keep their posture inside this contract: their dependencies (the
  database, the webhook secret outside development/test, the provider
  selection) remain hard dependencies of the gateway's aggregated `config`
  and `database` steps — expressed as readyz blockers instead of
  `os.Exit(1)`, because the process must keep serving liveness (the exact
  ADR-0008 rationale).
- **`Dockerfile.vercel`** builds/entrypoints the gateway by default
  (`ARG SERVICE=gateway`, `gateway` added to the allowed values; the
  per-service values remain for targeted builds). The root `Dockerfile`
  (compose/VPS) is intentionally untouched — compose keeps running the
  per-domain binaries it runs today; the gateway is the CONTAINER-topology
  entrypoint. `vercel.json` needs no change: `/v1/(.*)` already routes to
  the `app` container, which now serves every domain.

## Alternatives considered

1. **One container service per domain + per-prefix rewrites (option (b),
   rejected).** Five-plus Vercel container services mean: per-service env
   configuration duplicated on the project (the #122 env contract × N);
   N cold-start surfaces and N failure domains where there was one; Hobby-
   plan deployment quota burned by N build pipelines per commit; and a
   `vercel.json` rewrite table that must enumerate every domain prefix
   forever — the same class of ordering-sensitive, deploy-critical config
   that caused the #122 outage. Rejected unless the gateway hits a
   structural blocker (none did: the largest shared resource, PostgreSQL,
   was already shared by two domains in cmd/jobs).
2. **Per-service rewrites to the EXISTING single binary set** (route
   `/v1/vehicles/*` to a vehicles-built container, etc.) without a gateway.
   Same container count as (b) with the addition that identity-adjacent
   surfaces (`/.well-known/jwks.json`) and future cross-domain routes have
   no natural single owner. Superseded by (a).
3. **Forking the domain routers into one hand-written mux inside the
   gateway** (re-registering each domain's paths). Rejected on principle:
   any drift between the fork and the domain package silently changes the
   production API. The gateway calls the domain `Routes` constructors and
   nothing else.

## Consequences

- **Positive:** production serves the whole merged API surface from the
  topology it already runs; one env contract, one readiness story, one
  set of post-deploy probes; cross-domain routes (garages capacity,
  future #169 appointments) mount where their dependencies live; RBAC and
  auth posture are owned by exactly one place per domain.
- **Negative / accepted trade-offs:**
  - The gateway is a bigger blast radius: a permanent `config` blocker in
    ANY mounted domain's rules (identity secret, payments webhook secret
    in production, provider selection) keeps the WHOLE domain gate 503,
    not just that domain. Accepted: platform `Boot` blockers are
    process-global by design (ADR-0008), and a misconfigured domain was
    never servable anyway; per-domain partial readiness would require a
    platform change (track separately if the need arises).
  - Dispatch's documented no-database posture (score without persistence)
    is unavailable through the gateway: the shared database gates the
    whole process. Standalone `cmd/dispatch` remains the no-DB mode.
  - One process carries all domains' memory footprint; accepted at
    current scale and revisited if a domain needs isolation (the
    per-service build path stays available in `Dockerfile.vercel`).
- **Operational:** the RUNBOOK §6 contract table gains the gateway
  subsection (prefix → domain); the env contract on the Vercel project
  gains the identity-issuer variables (MOTIVRA_JWT_SECRET,
  MOTIVRA_JWT_PRIVATE_KEY_FILE — the gateway issues tokens) and the
  payments variables documented in backend/payments/README.md.

## Verification

- `cmd/gateway/main_test.go` (no database required): healthz 200 before
  dependencies; readyz 503 with machine-readable blocker names and no
  reason leakage; domain gate 503 before wiring; unknown `/v1` prefix →
  404 problem+json; wrong method → 405 problem+json; the full
  productionMounts route table mounted through the REAL constructors
  (`chi.Walk` over every prefix above); dispatch scoring reached through
  the production mount path with its RBAC gate enforced (dispatcher 200,
  customer 403, anonymous 401); garages capacity round-trip through the
  real garages service (GARAGE + tenant claims; foreign roles 403,
  anonymous 401).
- `cmd/gateway/integration_test.go` (pgtest-gated, `TEST_DATABASE_URL`):
  the EXACT production boot — config, database, all six migration chains,
  validator, routes — then one successful request per `/v1` prefix against
  the real stores (register→201, vehicle create→201, request create→201,
  capacity PUT/GET→200, payment intent→201, templates list→200, dispatch
  score→200, unknown prefix→404 problem+json). Skips cleanly without a
  database per the deferral-3 contract.
- `Dockerfile.vercel` SERVICE guard extended; root `Dockerfile` untouched
  (compose contract unchanged, verified by inspection).
