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
go run ./cmd/dispatch      # :8080  — stateless scoring (no DB client)
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

Run them against the local dev stack:

```bash
make dev                                                            # postgres on 5432, nats client on 4222
export TEST_DATABASE_URL='postgres://motivra:motivra@localhost:5432/motivra?sslmode=disable'
export TEST_NATS_URL='nats://localhost:4222'
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
| dispatch | `127.0.0.1:8084 : 8080` | stateless; no DB client |

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

---

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

## 6. Troubleshooting

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

## 7. Known limitations

Be honest with stakeholders about what this runbook does **not** cover yet:

- **CI is not executable** — the GitHub Actions billing lock (issue #10)
  means local gates (build/vet/test/lint, migration validation) are the
  merge gate. No CI badge should be trusted until #10 is resolved.
- **Helm charts / OpenTofu / Argo CD / canary deploys do not exist yet.**
  The deployment surface is this Dockerfile + compose stack; Kubernetes
  manifests, IaC and progressive delivery are future waves (see
  `infrastructure/README.md` "Not yet" list and `docs/ROADMAP.md`).
- **No TLS, domains or ingress** — everything binds plaintext HTTP on
  localhost ports. Terminating TLS, DNS and real secrets management are
  not configured.
- **Temporal is out of the app stack** — dev profile only
  (`make dev-temporal`); no service currently dials Temporal in production
  topology.
- **Redis env var is wired but idle** — no service consumes Redis yet.
  NATS event publishing is wired (issue #28): identity/jobs/vehicles build a
  JetStream publisher when `MOTIVRA_NATS_URL` is set and publish
  `motivra.<domain>.<aggregate>.<event>.v1` envelopes per ADR-0002; with the
  variable unset they run with a nil publisher. No domain consumes events yet
  (consumers land with the notifications wave).
- **One shared database in the stack** — services run per-domain migration
  chains in one Postgres database; per-domain DB roles/isolation (ADR-0003
  "own database role") are not yet provisioned.
- **Seed covers identity/vehicles/jobs only** — domains without migration
  chains (garages, estimates, payments, ...) cannot be seeded.
