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
service: `GET /healthz` (liveness), `GET /readyz` (dependency checks),
`GET /metrics` (Prometheus).

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

Host ports (chosen to coexist with `make dev`):

| Service | Host : container | Notes |
| --- | --- | --- |
| postgres (PostGIS) | `5433 : 5432` | `pg_isready` healthcheck |
| redis | `6380 : 6379` | |
| nats | `4223 : 4222`, `8223 : 8222` | JetStream `-js`; monitor `-m 8222` |
| identity | `8081 : 8080` | healthcheck `/healthz` |
| vehicles | `8082 : 8080` | healthcheck `/healthz` |
| jobs | `8083 : 8080` | healthcheck `/healthz` |
| dispatch | `8084 : 8080` | stateless; no DB client |

First boot takes a minute: each service waits for postgres health, then
applies its own migration chain inside the shared `motivra` database.

Build a single image by hand:

```bash
make docker-build SVC=identity    # SVC ∈ {identity, vehicles, jobs, dispatch}
```

---

## 4. Seeding demo data

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
- There is **no `garages` table yet** (`backend/migrations/garages/` is a
  stub chain) — the garage is represented by its GARAGE-role user until
  that domain ships.

---

## 5. Smoke tests

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
