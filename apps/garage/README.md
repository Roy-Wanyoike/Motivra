# Motivra Garage

The **partner garage console** (issue #124): the job board of work assigned to
the garage, a read-only invoice/ledger view from the payments foundation, and
the honest scaffolding for appointments and capacity. Next.js 16 +
TypeScript + Tailwind 4, mirroring `apps/command` conventions (deny-by-default
proxy gate, server-side upstream calls, per-panel degraded states).

## Honest status

| Panel | Status | Source |
| --- | --- | --- |
| Job board | **Wired — RBAC gap** | Calls the existing `GET /v1/jobs`; the endpoint currently answers 403 for the GARAGE role (operator-only list RBAC), so the panel renders its honest "backend capability pending" state until the garage-scoped read ships — [#165](https://github.com/Roy-Wanyoike/Motivra/issues/165) |
| Invoices (ledger) | **Live (read-only)** | `GET /v1/payments/intents` + `GET /v1/payments/intents/{id}/transactions` (contracts/payments) — tenant-scoped server-side by the token claims; a dedicated invoice-document aggregate does not exist, this IS the ledger (#124 acceptance: "invoice list matches ledger entries") |
| Appointments | **Intentionally disabled** | No appointments API/storage exists anywhere in the backend (verified across `contracts/` + `cmd/*` + migrations); the lifecycle is designed + unit-tested (`src/lib/appointments.ts`) and previewed in the panel — tracked in [#169](https://github.com/Roy-Wanyoike/Motivra/issues/169) |
| Capacity | **Intentionally disabled** | No capacity API/storage exists in the backend; the intended surface (bays, hours, technician slots) is described in the panel — tracked in [#170](https://github.com/Roy-Wanyoike/Motivra/issues/170) |

No demo/seed data anywhere: panels render live responses, real empty states,
or explicit pending/degraded states — never fake rows.

## Access model (deny-by-default) and multi-tenant isolation

- `src/proxy.ts` (Next.js 16 middleware, Node.js runtime) gates every route:
  anonymous/invalid → redirect `/login`; authenticated non-GARAGE → **403**
  on every console route; GARAGE → pass.
- The console admits **exactly one role: `GARAGE`**
  (`src/lib/auth/staff.ts`). Operators have `apps/command`; widening this
  allowlist is an identity RBAC-matrix decision first (the console adds a
  gate, never widens one — ADR-0004).
- Every server page **independently re-verifies** the session
  (`src/lib/auth/page-guard.ts`) — middleware alone is never the only check
  (ADR-0004 two-layer rule).
- **The garage identity is never client input.** The garage a session
  operates is exclusively the verified access token's `tenant_id` claim
  (`src/lib/auth/garage.ts`); no panel accepts a garage/tenant parameter,
  and upstream calls carry no garage identifier — the services derive the
  same boundary from the token claims (`backend/jobs/scope.go` ReadScope,
  `backend/payments/scope.go` ScopeFromClaims). A GARAGE token without a
  tenant claim is admitted but bound to nothing: every data panel fails
  closed (empty/degraded state), never a prompt to type a garage in.
- Tokens: asymmetric EdDSA/RS256 verified against the identity JWKS
  (public keys only, ADR-0007). Access + refresh tokens live in
  **httpOnly cookies** (refresh scoped to `/api/auth`); browser code never
  sees a token.
- PII: the read models carry no names/phones — identifier references are
  masked by default (`src/lib/pii/mask.ts`); console views and auth events
  are audit-logged (structured stdout → Loki). Money renders from integer
  KES minor units with pure integer math (`formatKes`) — never floats.

## Run

```bash
npm install
cp .env.example .env.local    # adjust service URLs for your topology
npm run dev                   # http://localhost:3000
npm run test                  # vitest (RBAC matrix, appointment lifecycle, panels)
npm run build && npm start    # production build
```

The backend comes up per docs/RUNBOOK.md §2 (`make dev` + `go run ./cmd/identity …`);
host-run defaults here match the RUNBOOK port map (identity :8081, jobs :8083,
payments :8085). A seeded garage staff account for local testing exists in
RUNBOOK §4 (`garage@demo.motivra.app`, GARAGE role scoped to the demo tenant).

## Layout

```
src/proxy.ts                  deny-by-default route gate (Next 16 middleware)
src/lib/auth/                 GARAGE allowlist, JWT/JWKS verify, session cookies, guards
src/lib/auth/garage.ts        garage identity from token claims (isolation core)
src/lib/api/                  server-side upstream clients (jobs, payments, problem+json)
src/lib/appointments.ts       appointment lifecycle state machine (pure, unit-tested, unwired)
src/lib/pii/mask.ts           identifier masking (PII-HANDLING.md)
src/lib/audit/audit.ts        structured audit events (auth + console views)
src/components/panels/        the four panels (plain components, server-fed props)
tests/                        vitest: RBAC matrix, garage identity, lifecycle, panels
```
