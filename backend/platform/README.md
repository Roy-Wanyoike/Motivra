# backend/platform

Shared Go foundation for every Motivra backend service. Contains **no
business logic** and imports **no domain packages** (ADR-0001).

## What it provides

| File | Responsibility |
|---|---|
| `config.go` | Typed environment configuration with aggregate validation |
| `logging.go` | slog JSON logging with request/tenant/trace context helpers |
| `errors.go` | RFC 7807-style API errors (`application/problem+json`), JSON body decoding |
| `database.go` | pgx pool builder + golang-migrate runner for per-domain `fs.FS` chains |
| `jwt.go` | Access-token validation (HS256, issuer-agnostic; identity service issues) |
| `events.go` | Domain-event envelope (ADR-0002), JetStream publisher, idempotent consumer, dedupe store |
| `otel.go` | OpenTelemetry tracing (OTLP) + Prometheus metrics exposition |
| `server.go` / `middleware.go` | chi router conventions: request ID, real IP, tracing, request logs, panic recovery, timeouts, `/healthz`, `/readyz` |
| `auth.go` | Auth + RBAC middleware (`RequireAuthenticated`, `RequireRole`), tenant scoping |
| `shutdown.go` | Graceful SIGTERM/SIGINT shutdown with ordered cleanup |
| `temporal.go` | Temporal client dialing (workflows live in owning domains only) |

## Starting a new service

1. Copy `cmd/template` to `cmd/<service>/main.go`.
2. Add an `//go:embed migrations` fs in the service package and call
   `platform.MigrateUp(ctx, pool, migrationsFS, "schema_migrations")` at boot.
3. Use `platform.ErrValidation/ErrNotFound/...` + `platform.WriteError` for
   every error response — problem+json everywhere, no bespoke formats.
4. Publish domain changes as `platform.Event` via `platform.Publisher`;
   consume idempotently via `platform.Consume` + a `DedupeStore`.
5. Scope every multi-tenant query with `platform.TenantScopeFrom(r)`.

## Do NOT

- Do not add business/domain logic or imports of domain packages.
- Do not issue JWTs here — identity owns issuance; everyone validates.
- Do not put money or vehicle history anywhere but the authoritative store.
- Do not swallow errors: convert and return them; the platform renders.
