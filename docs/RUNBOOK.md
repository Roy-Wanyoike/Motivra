# Motivra Runbook — running the platform locally and in the full stack

Audience: engineers onboarding to Motivra, and anyone demoing the platform.
Scope: everything that exists **today** on `main`. Anything not built yet is
listed in [Known limitations](#known-limitations) rather than promised here.

Companion artifacts (issues #25, #31): `Dockerfile`, `docker-compose.app.yml`,
`.env.example`, `scripts/seed_demo.sh`, `Makefile` targets (`docker-build`,
`stack-up`, `stack-down`, `seed`).

---

## 1. Prerequisites

| Tool | Version | Needed for |
| --- | --- | --- |
| Go | `1.27.1` (pinned in `go.mod`) | building/running services locally |
| Docker + Docker Compose v2 | recent stable | `make dev`, `make stack-up`, `make docker-build` |
| psql | PostgreSQL 16 client | `scripts/seed_demo.sh` |
| make, bash, curl | any | targets, seed, smoke tests |
| golangci-lint | per `.golangci.yml` | `make lint` (CI-equivalent gate) |

---

## 2. Local dev path (run services on the host)

Dev dependencies only (no app containers): `docker-compose.dev.yml` via make.

```bash
make dev                 # postgres:16, redis:7, nats:2.10 (JetStream), minio
make dev-temporal        # optional: adds Temporal server + UI (profile "temporal")
```

Connections: `postgres://motivra:motivra@localhost:5432/motivra`,
`redis://localhost:6379`, `nats://localhost:4222`.

Run one service (each service runs **its own migration chain** at boot —
`schema_migrations_identity`, `schema_migrations_vehicles`, `schema_migrations_jobs`):

```bash
export MOTIVRA_DATABASE_URL='postgres://motivra:motivra@localhost:5432/motivra?sslmode=disable'
export MOTIVRA_JWT_SECRET='dev-only-insecure-jwt-secret-change-me-32b'   # any >=32 bytes in prod
go run ./cmd/identity      # :8080  — auth, tokens, RBAC
go run ./cmd/vehicles      # :8080  — registry, history, passport
go run ./cmd/jobs          # :8080  — requests, state machine, assignments
go run ./cmd/dispatch      # :8080  — stateless scoring; with MOTIVRA_DATABASE_URL
                           set, scoring decisions are appended to audit_log (#88)
```

Env names are the contract in `backend/platform/config.go`; copy
`.env.example` for the full annotated list. Useful endpoints on every
service: `GET /healthz` (liveness), `GET /readyz` (dependency checks).
Prometheus metrics live on a separate internal listener
(`MOTIVRA_METRICS_ADDR`, default `:9090`) — never on the public API port.

Stop the dev stack: `make dev-down`.

### 2a. Gated test suites (Postgres + NATS integration)

The integration suites are gated behind environment variables and skip automatically (with a clear message) when unset:

| Suite | Gate variable |
|---|---|
| Store integration tests via `backend/platform/pgtest` (identity, jobs, vehicles) | `TEST_DATABASE_URL` |
| Platform NATS end-to-end publish test | `TEST_NATS_URL` |
| Redis sliding-window limiter tests via `backend/platform/redistest` (issue #87: allow/reject, cross-instance consistency, 429 middleware) | `TEST_REDIS_URL` |

Run them against the local dev stack:

```bash
make dev                                                            # postgres on 5432, nats client on 4222, redis on 6379
export TEST_DATABASE_URL='postgres://motivra:motivra@localhost:5432/motivra?sslmode=disable'
export TEST_NATS_URL='nats://localhost:4222'
export TEST_REDIS_URL='redis://localhost:6379/0'
go test ./backend/...
```

`pgtest` applies the per-domain migration chains (ADR-0003) before the tests run, so no manual migration step is needed. These suites execute automatically in CI once Actions billing is restored (issue #10).

### 2b. DCO sign-off gate (contribution hygiene)

Every content commit must carry a `Signed-off-by:` trailer matching its author (DCO 1.1 — see `CONTRIBUTING.md` and ADR-0006). CI enforces this on pull requests and pushes via the `dco` job (issue #54); run the same check locally before pushing:

```bash
bash scripts/check_dco.sh origin/main        # verify your branch's commits
git commit -s                                # add the sign-off when committing
```

The script skips merge commits and GitHub web-flow commits (no content authorship), and fails with a rebase recipe when a commit is unsigned or mismatched. Commits merged before the gate existed are grandfathered (rationale on issue #54); all new commits must pass.

---

## 3. Full-stack path (everything in containers)

`docker-compose.app.yml` runs PostGIS 16-3.4 + Redis + NATS (JetStream) +
the four services built from `./Dockerfile` (build-arg `SERVICE`).
**PostGIS is deliberate** — the architecture requires it (ADR-0003:
"PostGIS provides the geospatial substrate"). Temporal is intentionally not
in this stack yet (see limitations).

```bash
cp .env.example .env        # optional; adjust secrets before any shared use
make stack-up               # = docker compose -f docker-compose.app.yml up -d
```

Host ports (chosen to coexist with `make dev`). **All host bindings are
loopback-only (`127.0.0.1`)**: reachable from the host for local workflows
(`make seed`, curls) but never exposed on external interfaces when the
stack runs on a server — front the API ports with a reverse proxy/TLS
instead (issue #82). App services also get compose readiness healthchecks
(`/readyz`) and memory/CPU limits:

| Service | Host : container | Notes |
| --- | --- | --- |
| postgres (PostGIS) | `127.0.0.1:5433 : 5432` | `pg_isready` healthcheck |
| redis | `127.0.0.1:6380 : 6379` | |
| nats | `127.0.0.1:4223 : 4222`, `127.0.0.1:8223 : 8222` | JetStream `-js`; monitor `-m 8222` |
| identity | `127.0.0.1:8081 : 8080` | readiness `/readyz` |
| vehicles | `127.0.0.1:8082 : 8080` | readiness `/readyz` |
| jobs | `127.0.0.1:8083 : 8080` | readiness `/readyz` |
| dispatch | `127.0.0.1:8084 : 8080` | stateless scoring; appends decision records to `audit_log` (#88) |

First boot takes a minute: each service waits for postgres health, then
applies its own migration chain inside the shared `motivra` database.

Build a single image by hand:

```bash
make docker-build SVC=identity    # SVC ∈ {identity, vehicles, jobs, dispatch}
```

---

## 4. Seeding demo data

> **PRODUCTION WARNING — read before running any command in this section.**
> Demo seed data carries a **public** password (`MotivraDemo!2026`) and
> role-bearing demo accounts. It must never exist in a database that serves
> live customers. The script enforces this: it refuses to run without
> `ALLOW_DEMO_SEED=1`, and refuses unconditionally when
> `MOTIVRA_ENV=production`. After any demo seeding session, wipe the target
> volume (`docker compose -f docker-compose.app.yml down -v`) before
> deploying anywhere customer-facing.

`scripts/seed_demo.sh` inserts, in **one transaction**: a demo tenant id,
one garage (its GARAGE-role admin — see note), three technicians, one
customer, two vehicles with four passport-history entries, and one job in
`ASSIGNED` with its full transition trail and one assignment. It is
idempotent (fixed UUIDs + `ON CONFLICT DO NOTHING` / `NOT EXISTS` guards),
never updates or deletes history rows (append-only triggers stay unprovoked),
and prints a created-summary.

```bash
make seed                                            # targets the app stack (localhost:5433)
bash scripts/seed_demo.sh postgres://motivra:motivra@localhost:5432/motivra?sslmode=disable   # dev-deps stack
```

Seeded logins (public demo credential — **not** a secret):
`customer@demo.motivra.app`, `tech1..tech3@demo.motivra.app`,
`garage@demo.motivra.app`, password `MotivraDemo!2026`.

Honest notes:
- There is **no `tenants` table yet** — the "demo tenant" is a fixed UUID
  used in `tenant_id` columns (`user_roles`, `vehicles`).
- There is **no `garages` table yet** (no `backend/migrations/garages/`
  chain exists) — the garage is represented by its GARAGE-role user until
  that domain ships.

---

## 4a. Smoke tests

Routes below are the real ones from `backend/*/http.go` (mirrored in
`contracts/*/openapi.yaml`). Run with the app stack up and seeded.

```bash
# 1) Health (any service; use its host port)
curl -sf http://localhost:8081/healthz      # {"status":"ok"}
curl -sf http://localhost:8081/readyz       # {"status":"ready"} — DB checked

# 2) Login (identity) — capture the access token
TOKEN=$(curl -sf http://localhost:8081/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"email":"customer@demo.motivra.app","password":"MotivraDemo!2026","device_name":"runbook-smoke"}' \
  | sed -n 's/.*"access_token":"\([^"]*\)".*/\1/p')
echo "$TOKEN" | cut -c1-20

# 3) Register a vehicle (vehicles)
curl -sf http://localhost:8082/v1/vehicles \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"vin":"1HGCM82633A004352","make":"Toyota","model":"Probox","year_of_manufacture":2016,"plate":"KDG 789C","color":"Blue"}'

# 4) Fetch the Vehicle Passport (vehicles) — use the id from step 3
curl -sf http://localhost:8082/v1/vehicles/<vehicleID>/passport -H "Authorization: Bearer $TOKEN"

# Bonus: seeded job (jobs) and dispatch factors
curl -sf http://localhost:8083/v1/jobs -H "Authorization: Bearer $TOKEN"
curl -sf http://localhost:8084/v1/dispatch/factors -H "Authorization: Bearer $TOKEN"
```

Expected: `200` on every call; the passport carries the seeded history
entries for the demo vehicles. `401` means the token is missing/expired;
`403` means the route requires a role the caller lacks (e.g. `GET /v1/jobs`
needs DISPATCHER/ADMIN/SUPER_ADMIN).

---

## 5. Monitoring & observability stack

The observability stack (issue #84, ADR-0005) ships as a second compose
file attached to the app network:

```bash
docker compose -f docker-compose.app.yml up -d        # app stack first
docker compose -f docker-compose.observability.yml up -d
```

| Surface | URL | Notes |
| --- | --- | --- |
| Grafana | http://localhost:3001 | anonymous admin — lock down for shared deployments |
| Prometheus | http://localhost:9095 | scrape targets + alert rules (`observability/alerts.yml`) |
| OTel collector | localhost:4317/4318 | app services export traces/logs here |

- **Metrics:** every service exposes RED metrics (`http_requests_total`,
  `http_request_duration_seconds`) on its internal `:9090` listener —
  never on the public port. Grafana has the "Motivra — Golden Signals"
  dashboard (rate, 5xx share, p95/p50 latency, scrape health).
- **Logs:** JSON to stdout, shipped via OTLP to Loki. Every request line
  carries `request_id`, `trace_id` and (authenticated) `tenant_id`.
- **Traces:** OTLP → Tempo; enable per service with `MOTIVRA_OTEL_ENABLED=true`
  and `OTEL_EXPORTER_OTLP_ENDPOINT=otel-collector:4317` in the app `.env`.
- **Alerts (respond per rule):** `ServiceDown` — check container state and
  `docker compose logs <svc>`; `High5xxRate` — pull the 5xx lines from
  Loki via the dashboard, correlate with the deploy timeline;
  `AuthThrottlePressure` — sustained auth 4xx suggests credential
  stuffing (the limiter is absorbing it) or a broken client release.
- Data ships to container-local volumes: mount durable storage and set
  retention before relying on this beyond evaluation.

## 5a. Data operations (backup, restore, retention, integrity)

Operational commands introduced by the integrity/compliance pass (issue
#83); full procedures live in the Backup & DR and PII sections (issue #85).

```bash
make db-backup                                 # timestamped custom-format dump to ./backups
make db-restore FILE=backups/<dump>.dump I_UNDERSTAND_THIS_DROPS_DATA=1   # DROPS the target DB
make integrity-check                           # per-table counts + orphan queries + trigger arms (must be all-clear)
make purge-expired PURGE_AUDIT=false           # retention dry run (counts eligible audit rows)
make purge-expired PURGE_AUDIT=true AUDIT_MONTHS=18   # actual audit purge (log the invocation!)
```

Rules:
- **Backups are not real until restored.** After every backup, run the
  restore into a scratch database and `make integrity-check` against it.
- **`audit_log` and `job_transitions` are append-only at the database
  level** (identity 000006 / jobs 000005): UPDATE/DELETE raise exceptions.
  The ONLY sanctioned audit deletion path is the guarded window inside
  `scripts/purge_expired.sql` with `PURGE_AUDIT=true`; record every such
  invocation in the operations log.
- **Retention schedule:** sessions purge = daily (cron); audit purge =
  monthly, 18-month window by default; integrity check = weekly and after
  every restore/cleanup.

---

### 5b. Exception events — the panic/error-tracking path (issue #88)

Panics never vanish into stdout alone. The platform's panic-recovery
middleware converts a recovered handler panic into the usual 500
problem+json AND records an **OpenTelemetry exception event** on the
request's server span (`exception.type`, `exception.message`,
`exception.stacktrace` per the OTel semconv) with the span marked Error;
the structured panic log line additionally carries `panic_type` and the
stack trace. The pipeline (ADR-0005: OTel-only, no vendor APM):

1. **Where to look:** Grafana (§5) → Tempo trace view. Filter for spans
   with status Error (or query Loki for the panic lines below) and open
   the trace — the exception event with its stack trace is on the server
   span, one click from the failing request.
2. **Log-side hunt:** Loki query `{service="identity"} |= "panic_recovered"`
   (substitute the service). Every recovered-panic line carries the panic
   value, `panic_type`, the stack trace, `request_id` and `trace_id`;
   paste the `trace_id` into Tempo to get the full request path around
   the crash.
3. **Triage:** treat any recovered panic as a bug (the 500 response hides
   internals from the caller by design — the trace is the debugging
   surface), correlate `exception.type` counts per service in the golden
   signals dashboard, and file with the stack trace attached.

A Sentry-style SaaS error tracker remains a **follow-up decision**, not
part of this change: the OTel exception events above already make panics
queryable in the deployed stack, and adopting a vendor would re-decide the
ADR-0005 posture (exporter swap at the collector) — revisit when crash
grouping/alerting across services is actually needed.

---

## 6. Production deployment

The compose app stack is the deployment surface. A production run differs
from a local one in four ways: real secrets, no host-exposed data layer,
TLS at the edge, and monitoring attached.

### Required environment (the `.env` contract)

`cp .env.example .env`, then set **at minimum**:

| Variable | Requirement |
| --- | --- |
| `POSTGRES_PASSWORD` | generated (`openssl rand -base64 24`) — compose refuses to boot without it |
| `MOTIVRA_JWT_PRIVATE_KEY_FILE` | path to the Ed25519/RS256 signing PEM (compose mounts `./secrets/`). Provision once: `mkdir -p secrets && openssl genpkey -algorithm ed25519 -out secrets/jwtSigningKey.pem`. Identity is the sole issuer (ADR-0007); a missing key file refuses boot |
| `MOTIVRA_JWT_SECRET` | generated (`openssl rand -base64 48`), ≥32 bytes; keys refresh-session digests (and the `MOTIVRA_JWT_ALLOW_HS256` migration window only). Validated in staging/production; the historical dev placeholder is refused everywhere |
| Verifier key source (each verifying service) | `MOTIVRA_JWT_JWKS_URL` (compose default: `http://identity:8080/.well-known/jwks.json`) or `MOTIVRA_JWT_PUBLIC_KEY_FILE`; verifiers hold NO secret material (ADR-0007). HS256 verification requires the explicit `MOTIVRA_JWT_ALLOW_HS256=1` migration flag — retire before public launch |
| `MOTIVRA_REDIS_URL` | set → the Redis-backed sliding-window rate limiter (cross-instance consistent, #87); unset → in-process limiter (single instance only) |
| `MOTIVRA_PAYMENTS_WEBHOOK_SECRET` | REQUIRED outside development/test — HMAC secret for payment webhook signatures; register the public callback URL with the provider (owner action) |
| `MOTIVRA_ENV` | `production` (enables the strict secret rules and OTEL-by-default) |
| `MOTIVRA_OTEL_ENABLED` + `OTEL_EXPORTER_OTLP_ENDPOINT` | `true` + `otel-collector:4317` once the observability stack runs |
| `MOTIVRA_TRUST_FORWARDED` | `true` only when a reverse proxy fronts the services; otherwise audit IPs and rate-limit keys are spoofable |
| `MOTIVRA_AUTH_RATE_LIMIT_RPM` | default 60; tune per expected traffic (0 disables — do not) |
| `MOTIVRA_HIBP_ENABLED` | `true` in production: enables the HIBP k-anonymity breach lookup (only the 5-char SHA-1 prefix leaves the process) on top of the always-on local denylist; default `false`. The local denylist answers even with HIBP off, and a failed lookup fails OPEN with an `identity.password.breach_check_unavailable` audit note unless `MOTIVRA_BREACH_FAIL_CLOSED=true` |
| `MOTIVRA_BREACH_FAIL_CLOSED` | default `false` (fail-open with audit note); set `true` to refuse registration/password change while the breach check cannot complete |
| `MOTIVRA_BREACH_DENYLIST_FILE` | optional path to extra denied passwords (one per line, `#` comments); an unreadable file refuses boot |
| `MOTIVRA_VERIFICATION_GRACE` | default `24h`: window after registration during which an unverified account may still mint/rotate refresh sessions; refresh is refused (403) after it ends until every present channel is verified. `0` disables the gate — do not in production |
| `MOTIVRA_VERIFICATION_TOKEN_TTL` | default `24h`: validity of single-use verification tokens; keep ≥ the grace window so a token issued at registration outlives it |
| `MOTIVRA_VERIFICATION_RESEND_COOLDOWN` | default `1m`: minimum spacing between verification tokens per user+channel |
| `MOTIVRA_VERIFICATION_RESEND_LIMIT` | default `5`: maximum verification tokens per user+channel in a rolling 24h window |
| `MOTIVRA_VERIFICATION_DEV_ECHO` | MUST stay unset in staging/production (boot refuses it there): echoes raw verification tokens in register/resend responses for provider-less local development |

### Boot sequence

1. Review the diff of `.env` against `.env.example` — no committed values.
2. `docker compose -f docker-compose.app.yml up -d` — services wait for
   postgres health (verifiers also wait for identity's JWKS), then apply
   their own migration chains at boot.
3. Attach monitoring: `docker compose -f docker-compose.observability.yml up -d`.
4. Wait for all six `/readyz` to return 200 (compose healthchecks gate on
   readiness): identity :8081, vehicles :8082, jobs :8083, dispatch :8084,
   payments :8085, inspections :8086.
5. Run the §4a smoke checklist against `127.0.0.1:<port>` from the host.
6. Verify **zero demo data**: `make integrity-check` and confirm
   `users`/`vehicles`/`jobs` counts match expectations for a fresh deploy
   (a fresh deploy has 0 rows in every domain table).

### TLS and the edge

- All host ports are loopback-bound (#82). Put a reverse proxy (Caddy,
  nginx, or a cloud LB) on the host for TLS termination and forward to
  `127.0.0.1:8081-8086`.
- The Vercel edge (for the customer web) carries HSTS preload and the
  security-header set; keep `vercel.json` and the proxy headers in sync.
  API paths are NOT served from Vercel — clients reach `/v1/*` on the
  platform's own domain once the backend host is public.
- Rate limiting lives in the identity service; add edge-level limits
  (per-IP connection/request caps) at the proxy for defense in depth.

### Vercel project configuration (customer web + backend posture)

Two Vercel projects build from this repository. Their dashboard settings are
part of the deployment contract — a wrong toggle here is invisible in git and
can silently take the product offline (#96, #99).

| Project | Builds | Required settings |
| --- | --- | --- |
| `motivra` | `apps/customer` (Next.js) | Root Directory `apps/customer`; framework Next.js (auto-detected); install command `npm install` |
| `motivra-services` | repository root (Dockerfile) | Root Directory repository root; framework Other; container build driven by the `services` block in root `vercel.json` |

**The root `vercel.json` `services` block is load-bearing** — empirically,
replacing it with a minimal headers-only config broke BOTH projects'
builds (#99: `motivra-services` lost its build input; deploys failed until
the prior shape was restored). Treat that file as deploy-critical: change
it only with Vercel build logs in front of you.

**The backend cannot serve production traffic from Vercel.** The Go
services are long-running processes that own their Postgres and NATS
dependencies; the containerized `motivra-services` deployment boots
without them, so its `/healthz` and `/readyz` return
FUNCTION_INVOCATION_FAILED and `/` has no UI route (404) — this is a
deployment-target mismatch, not an application defect (#99). The
supported backend surface is the compose stack behind a reverse proxy
(§6 above). The customer web project (`motivra`) is the only product UI.
Do not point API rewrites at Vercel and do not advertise the services
URL to users.

**Deployment Protection must be disabled for the production surface.**
Vercel → Project → Settings → Deployment Protection → *Vercel
Authentication*: **Disabled** (optionally keep it enabled for *preview
deployments only*). With authentication enabled, every request — including
the production alias — 302-redirects to `vercel.com/sso-api`, so the public
sees a Vercel login page or "page unavailable" instead of the app. This is
the exact failure mode behind #96 and it produces **no build error and no
commit-status failure**; it can only be caught by probing the public URL.

Environment variables (project `motivra`): `NEXT_PUBLIC_API_BASE_URL` is
optional while the customer app runs standalone — empty means same-origin,
which is correct once the `/v1/*` rewrites to the services deployment are
active. Set it to the services base URL only when that URL is publicly
reachable (or a protection-bypass secret is configured), otherwise API
calls fail behind the same SSO wall.

Post-deploy verification (run from a clean session, no Vercel cookies):

```sh
# 1) No SSO redirect — expect HTTP 200 and cache HIT/MISS, never 302 to vercel.com/sso-api
curl -sI https://<CANONICAL-FRONTEND-DOMAIN>/ | head -1
# 2) Landing content actually renders (static prerender — no function involved)
curl -s https://<CANONICAL-FRONTEND-DOMAIN>/ | grep -o "<title>[^<]*</title>"
# 3) Key routes exist
curl -s -o /dev/null -w "%{http_code}\n" https://<CANONICAL-FRONTEND-DOMAIN>/login
```

### Canonical public URL (verified 2026-10-14) and domain hygiene

The **only live, public, protection-free product surface** is:

> **`https://motivra-bfj9.vercel.app/`** — customer web (Next.js, `apps/customer`);
> verified `200` on `/`, `/login`, `/dashboard` with no SSO wall.

Every other URL observed in 2026-10 testing is broken or misleading, and the
root causes are **dashboard-side, not code-side**:

| URL observed | Behavior | Explanation |
| --- | --- | --- |
| `motivra-bfj9.vercel.app` | `200` | the working production surface (see above) |
| `motivra-youngsharksites.vercel.app` | `404 DEPLOYMENT_NOT_FOUND` | domain claimed by project `motivra` (team `youngsharksites`) but no production deployment currently assigned to it |
| `motivra.vercel.app` | `500` | a DIFFERENT project with the same name on the personal team `roywanyoikes-projects` |
| `motivra-rho.vercel.app` (and similar guesses) | `404 NOT_FOUND` | unclaimed domain — no project owns it; never existed |
| `motivra-services-*` | `404` / `FUNCTION_INVOCATION_FAILED` | backend container on Vercel — unsupported target (see above) |

**Root cause to fix in the Vercel dashboard:** the GitHub repo is connected
to TWO Vercel teams at once (`youngsharksites` AND the personal
`roywanyoikes-projects`). Consequences: duplicate projects with colliding
names, deployments fired twice per commit (burning the Hobby plan's 100
deployments/day quota — `api-deployments-free-per-day` failures), stale
protected/dangling domains, and user-visible 404/500 pages on URLs that
look authoritative. Owner actions:

1. Disconnect the GitHub integration from ONE of the two teams (keep
   `youngsharksites`; remove the personal account's `motivra` project).
2. In the remaining team, keep ONE canonical frontend project and pin its
   production domain (or attach a real custom domain) — then update the
   probes above and this section's URL.
3. Keep Deployment Protection disabled on the canonical production surface
   (per the section above).

### Known deployment posture

One shared Postgres cluster (ADR-0003) with per-domain migration chains;
PostGIS required; Redis-backed distributed rate limiting active when
`MOTIVRA_REDIS_URL` is set (issue #87); six services run their own
migration chains at boot (identity, vehicles, jobs, dispatch, payments,
inspections); Temporal intentionally absent from the stack yet.

---

## 7. Backup & DR

Commands live in §5a (`make db-backup` / `make db-restore` /
`make integrity-check`). This section defines the objectives.

| Objective | Target | Mechanism |
| --- | --- | --- |
| RPO (max data loss) | 24 h with nightly backups; ~0 with managed PG PITR | nightly `make db-backup` to durable storage; managed Postgres WAL archiving recommended for production |
| RTO (max downtime) | 30 min | restore into a fresh volume (`make db-restore`), `make integrity-check`, redeploy services |
| Backup verification | every backup | restore into scratch + integrity check before considering it real |

### Nightly backup (cron)

```
0 2 * * *  cd /srv/motivra && make db-backup && find ./backups -name '*.dump' -mtime +14 -delete
```

Ship `./backups` off-host (object storage) — a backup on the same host as
the database is not a backup.

### Restore procedure (tested path)

1. Provision scratch: `docker run -d --name pg-restore-check -e
   POSTGRES_PASSWORD=x -p 127.0.0.1:5499:5432 postgis/postgis:16-3.4`.
2. `make db-restore FILE=<dump> DB_URL=postgres://...:5499/...` (requires
   `I_UNDERSTAND_THIS_DROPS_DATA=1`).
3. `make integrity-check DB_URL=...:5499/...` — all orphans zero, triggers
   armed.
4. Point the stack at the restored volume only after the check passes.

### Disaster recovery

- **Bad deploy:** redeploy previous image tag; migrations are additive
  (each chain ships downs) — never downgrade schema without a reviewed
  plan; additive migrations make forward-only recovery the default.
- **Data corruption:** stop services, snapshot the volume for forensics,
  restore latest verified backup, replay via §5a.
- **Secret compromise:** rotate per §8, then follow §9.

---

## 8. Secrets rotation

| Secret | Rotation trigger | Procedure | Impact |
| --- | --- | --- | --- |
| JWT signing key (`MOTIVRA_JWT_PRIVATE_KEY_FILE`, ADR-0007) | quarterly, suspected compromise, kid retirement | §8a procedure below (two-phase `kid` rotation; compromise = drop the kid immediately) | access tokens signed by the retired key are refused within one access TTL (15 min) of the kid leaving the JWKS; refresh sessions unaffected (opaque, unsigned) |
| `MOTIVRA_JWT_SECRET` | quarterly, suspected compromise, team change | generate new 48-byte secret; update `.env`; rolling restart of identity during a low-traffic window | since ADR-0007 the secret no longer signs access tokens (EdDSA does) — ALL refresh sessions die (digests are HMAC'd with the secret; every lookup misses), users re-authenticate; purge orphaned session rows via §5a. Verifiers no longer hold the secret at all; it keys refresh digests and the `MOTIVRA_JWT_ALLOW_HS256` migration path only |
| `POSTGRES_PASSWORD` | quarterly, suspected compromise | change in Postgres (`ALTER USER`), update `.env`, restart the stack | brief downtime during restart |
| TLS certificates | expiry | proxy-managed (ACME) | none if automated |
| GitHub PAT | immediately on any exposure | regenerate in GitHub settings, update deploy secrets | CI/deploy pause until updated |

Compromise-specific: after any suspected secret leak, rotate FIRST
(sessions/tokens die), then investigate with §9 — never investigate on
possibly-compromised credentials.

### 8a. JWT signing-key (`kid`) and JWKS rotation (ADR-0007, issue #87)

Signing is asymmetric: identity signs with Ed25519/RS256; verifying services
hold public keys only — the JWKS at `/.well-known/jwks.json` and/or a
distributed public-key file — selected by the token's `kid`. Rotation needs
no restarts and no secret synchronization.

**Two-phase rotation (planned, zero downtime):**

1. **Introduce the new key.** Provision the new PEM (`MOTIVRA_JWT_PRIVATE_KEY_FILE`, new derived or explicit `MOTIVRA_JWT_KEY_ID`) and restart identity while the JWKS still serves BOTH kids. Verifiers pick the new kid up on first sighting (unknown-kid refetch, bounded to ≤1 fetch/second per instance; first fetch always bypasses the bound). New logins/refreshes now mint new-kid tokens.
2. **Retire the old kid.** After one access TTL (15 min default) every token in the wild is signed by the new key — remove the old key from the served set. Old-key tokens fail at the next validation, so **old tokens are invalid within one access TTL of completing phase 1**. Refresh sessions keep working (opaque tokens are not signed).

**Compromise (skip phase 1):** drop the compromised kid from the JWKS/key set
immediately — its tokens die at every verifier's next validation, well within
one access TTL; then rotate `MOTIVRA_JWT_SECRET` per §8 (refresh sessions
must die too) and follow §9.

**JWKS fetch cadence (what verifiers do automatically):**

- cache warms at boot and `/readyz` probes the `jwks` checker — a red `/readyz` on a verifier means its JWKS fetch is failing;
- whole-set refresh every 5 minutes (`jwksCacheTTL`); unknown-kid refetch bounded to 1/second (`jwksMinRefreshGap`) — forged-`kid` floods cannot amplify into the issuer;
- on JWKS outage verifiers serve the stale key set (auth keeps working) while `/readyz` goes red — restore the endpoint, no restart needed.

**Verify a rotation:** list the served kids (`curl .../.well-known/jwks.json | jq '.keys[].kid'` — expect both kids in phase 1, only the new kid in phase 2), confirm verifier logs show no `unknown kid` errors, and confirm `rate_limit_decisions_total`/auth metrics stay flat (no rejection spike beyond the expected old-key tail).

---

## 9. Incident response

Severity skeleton (calibrate when on-call is staffed):

| Level | Definition | Response |
| --- | --- | --- |
| SEV1 | Live customers blocked (all auth failing, data loss, confirmed breach) | immediate: mitigate (rollback/restart/rotate) → comms to affected users → postmortem within 72 h |
| SEV2 | Degraded core flow (one service down with fallback, elevated 5xx) | same-day mitigation, root cause within 48 h |
| SEV3 | No customer impact (redundancy issues, alert noise) | batch into the next work cycle |

First-response checklist:

1. **Identify**: Grafana golden-signals dashboard (§5) — which service,
   which signal, since when. `ServiceDown`/`High5xxRate` alerts are the
   tripwires.
2. **Contain**: `docker compose -f docker-compose.app.yml restart <svc>`;
   rollback via previous image if a deploy caused it.
3. **Preserve**: `docker compose logs <svc> > incident-<date>.log` and a
   volume snapshot BEFORE destructive actions.
4. **Communicate**: note status in the incident channel; SEV1 requires
   customer comms before the day ends.
5. **Close**: postmortem for SEV1/2 — timeline, root cause, action items
   filed as issues; update this runbook if procedures changed.

Data-breach specific (DPA 2019 notification duties): involve the data
controller/owner immediately; document scope via §5a integrity checks and
audit_log review before notifying.

---

## 10. Troubleshooting

| Symptom | Likely cause / fix |
| --- | --- |
| Service exits: `MOTIVRA_DATABASE_URL is required` | Env var not set — every service (incl. dispatch, by validation) needs it outside `test` env. |
| Service exits: `invalid configuration: ... JWT_SECRET ...` | Secret missing or `< 32 bytes` with `MOTIVRA_ENV=production`. |
| `readyz` returns 503 with `"failed":{"database":...}` | Postgres down or wrong URL; check `docker compose -f docker-compose.app.yml ps` and logs. |
| `401 invalid token` between services | All services must share the same `MOTIVRA_JWT_SECRET` + issuer + audience (compose defaults align them). |
| Login returns validation error | Password policy: min 10 chars (identity `MinPasswordLength`). |
| VIN rejected on register | `vin_normalized` uniqueness + identity-service VIN validation (`backend/vehicles/vin.go`); seeded VINs are reserved. |
| Seed: `error: psql not found` | Install the PostgreSQL client, or run seed inside a container with psql. |
| Seed: connection refused on 5433 | App stack not up (`make stack-up`), or you meant the dev stack on 5432. |
| Port clash when both stacks run | They are designed to coexist (5433/6380/4223 vs 5432/6379/4222); stop the other stack if you changed mappings. |
| `make dev` + `make stack-up` NATS monitor clash | Unlikely (8223 vs 8222); if you remapped ports, keep them disjoint. |
| Logs | `docker compose -f docker-compose.app.yml logs -f identity` (JSON slog lines, request ids). |

---

## 11. Known limitations

Be honest with stakeholders about what this runbook does **not** cover yet
(resolved items from the production-readiness pass are at the bottom):

- **CI is blocked by the GitHub Actions billing lock (issue #10)** — the
  workflow file is complete (DCO, lint, Go tests with Postgres, migration
  validation, customer/technician app gates, gitleaks) and enabled, but
  jobs fail at scheduling until the owner resolves billing. Local gates
  remain the merge gate; no CI badge should be trusted until then.
- **Helm charts / OpenTofu / Argo CD / canary deploys do not exist yet.**
  The deployment surface is this Dockerfile + compose stack; Kubernetes,
  IaC and progressive delivery are future waves (see `docs/ROADMAP.md`).
- **TLS terminates at a reverse proxy you provide** — services bind
  plaintext HTTP on loopback ports by design (#82); the edge (Vercel for
  web + `/v1/*`) carries HSTS. Host-level TLS/DNS is an operator step
  (§6).
- **Temporal is out of the app stack** — dev profile only
  (`make dev-temporal`); no service currently dials Temporal in production
  topology.
- **Redis env var is wired but idle** — rate limiting is in-process
  (#81); Redis-backed distributed limiting for horizontal scale is
  tracked in #87. NATS event publishing is wired (issue #28): services
  publish `motivra.<domain>.<aggregate>.<event>.v1` envelopes when
  `MOTIVRA_NATS_URL` is set; no domain consumes events yet (notifications
  wave).
- **One shared database in the stack** — services run per-domain migration
  chains in one Postgres database; per-domain DB roles/isolation (ADR-0003
  "own database role") are tracked in #88.
- **Seed covers identity/vehicles/jobs only** — domains without migration
  chains (garages, estimates, payments, ...) cannot be seeded.
- **Observability storage is evaluation-grade** — container-local volumes
  in the compose stack; mount durable storage and set retention before
  relying on it (§5).

Resolved by the production-readiness pass (#80–#85): demo-data gating,
auth rate limiting, secret policy, internal metrics, fail-closed scoping,
absolute session caps, append-only audit/transitions, backup/restore +
retention + integrity tooling, RED metrics + log correlation +
monitoring stack, security headers, loopback-bound ports, frontend CI
jobs + secret scanning (pending billing), and this runbook's production
sections plus docs/PII-HANDLING.md.
