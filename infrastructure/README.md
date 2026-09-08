# Infrastructure

Deployment and release infrastructure: Docker, Kubernetes/Helm, OpenTofu,
Argo CD and CI/CD configuration. The platform/SRE engineer owns this zone.

Owned by the infrastructure owner; see docs/ARCHITECTURE.md §2 — do not edit outside your zone.

## What exists today (issues #25 + #31 — deployment readiness baseline)

| Artifact | Purpose |
| --- | --- |
| `/Dockerfile` | Multi-stage image builder, one image per service via `--build-arg SERVICE` (`identity` default; `vehicles`, `jobs`, `dispatch`). Build stage pinned to `golang:1.27.1-alpine` (matches `go.mod`); runtime `alpine:3.21`, ca-certificates, non-root UID/GID 10001, `HEALTHCHECK` on the real platform liveness path `GET /healthz`. Static build: `CGO_ENABLED=0`. |
| `/docker-compose.app.yml` | Full application stack: `postgis/postgis:16-3.4` (PostGIS is an architecture requirement — ADR-0003), `redis:7-alpine`, `nats:2-alpine` with JetStream (`-js -m 8222`), and the four services with env wiring that matches `backend/platform/config.go` exactly. Named network + volumes, healthchecks, `depends_on` conditions. Temporal intentionally excluded (commented note in-file). |
| `/.env.example` | Every key the platform config actually reads, annotated; no real secrets. Compose interpolation keys (`POSTGRES_*`) included. |
| `/docs/RUNBOOK.md` | Prerequisites, local dev path, full-stack path, seeding, smoke tests (health → login → register vehicle → passport), troubleshooting, known limitations. |
| `/scripts/seed_demo.sh` | Idempotent, single-transaction demo dataset against the real migration schema (see issue #31); append-only history respected; prints a created-summary. |
| `Makefile` (appended targets) | `docker-build SVC=…`, `stack-up`, `stack-down`, `seed`. |

Design notes:

- **Env contract**: `backend/platform/config.go` is the single source of
  truth for configuration (`MOTIVRA_*`, `OTEL_EXPORTER_OTLP_ENDPOINT`).
  The compose file and `.env.example` mirror it verbatim; the cross-check
  table in PR #26 (deployment readiness) records the grep evidence.
- **Health**: platform servers mount `GET /healthz` (liveness) and
  `GET /readyz` (readiness with dependency checkers) —
  `backend/platform/server.go`. Container healthchecks use `/healthz`;
  orchestrators that gate on dependencies should probe `/readyz`.
- **Migrations run at service boot** (`platform.MigrateUp` per domain
  chain, ADR-0003) — no separate migrate job is needed in this stack.
- **Host ports** are offset (5433/6380/4223/8081-8084) so the app stack and
  the dev-dependencies stack (`docker-compose.dev.yml`) can coexist.
- **Dispatch caveat**: stateless, but `platform.Config.Validate()` requires
  `MOTIVRA_DATABASE_URL` outside the `test` env, so compose passes it
  (present-but-unused) — documented in the compose file.

## Not yet (honest status — do not assume these exist)

- **Kubernetes manifests / Helm chart** — nothing charted; the compose stack
  is the only orchestrator-ready artifact.
- **OpenTofu (IaC)** — no cloud provisioning definitions at all.
- **Argo CD / GitOps** — no continuous-delivery pipeline configured.
- **Canary / progressive delivery** — no traffic-shaping machinery.
- **TLS, domains, ingress, secrets management** — plaintext localhost only;
  real secret values must never enter this repo (see `.env.example`).
- **GitHub Actions CI** — wired in `.github/workflows/ci.yml` but blocked
  account-wide by the billing lock (issue #10); local gates
  (`make build/lint/test`, `make validate-migrations`) are the interim
  merge gate. This section will be updated when Actions run again.
- **Temporal in the app stack** — dev profile only
  (`make dev-temporal`); app-stack wiring lands with the first workflow
  consumer (`MOTIVRA_TEMPORAL_ADDRESS` is deliberately unset in compose).
- **Per-domain DB roles** — services share one database in the stack;
  ADR-0003's "own database role per domain" hardening is future work.
