# Motivra Command

The **staff-only internal operations console** (issue #123): live job board
from `cmd/jobs` + `cmd/dispatch` read APIs, an escalations queue for jobs in
blocked/escalated states, and a system-health panel aggregating `/healthz` +
`/readyz` for every `cmd/*` service. Next.js 16 + TypeScript + Tailwind 4,
mirroring `apps/customer` conventions.

## Honest status

| Panel | Status | Source |
| --- | --- | --- |
| Job board | **Live** | `GET /v1/jobs` (contracts/jobs 1.1.0 — status optional + `technician_id` filter added for this console) |
| Dispatch factors | **Live** | `GET /v1/dispatch/factors` (contracts/dispatch) — rendered beside the board for explainability |
| Escalations queue | **Live** | `GET /v1/jobs?status=` for `ESCALATED` / `AWAITING_APPROVAL` / `FAILED`; age = time in state |
| System health | **Live** | `/healthz` + `/readyz` of identity, vehicles, jobs, dispatch, inspections, payments |
| Incidents | **Intentionally absent** | No incidents read API exists in the backend (verified across `contracts/` + `cmd/*`); shipping one would require new migrations, which this MVP forbids — tracked in [#128](https://github.com/Roy-Wanyoike/Motivra/issues/128) |
| Mutations (transition/assign from the console) | **Deferred** | Read-first MVP; console mutations = follow-up issue |

## Access model (deny-by-default)

- `src/proxy.ts` (Next.js 16 middleware, Node.js runtime) gates every route:
  anonymous/invalid → redirect `/login`; authenticated non-staff → **403** on
  every console route; staff → pass.
- Staff roles are exactly the jobs service' operator read scope —
  `DISPATCHER`, `ADMIN`, `SUPER_ADMIN` (`src/lib/auth/staff.ts`) — so every
  staff user sees live data on every panel. Extending access (e.g. SUPPORT)
  is an identity RBAC-matrix decision first.
- Every server page **independently re-verifies** the session
  (`src/lib/auth/page-guard.ts`) — middleware alone is never the only check
  (ADR-0004 two-layer rule).
- Tokens: asymmetric EdDSA/RS256 verified against the identity JWKS
  (public keys only, ADR-0007). Access token + refresh token live in
  **httpOnly cookies** (refresh scoped to `/api/auth`); browser code never
  sees a token; no long-lived tokens exist anywhere client-side.
- PII: the read model carries no names/phones — identifier references are
  masked by default (`src/lib/pii/mask.ts`); console views and auth events
  are audit-logged (structured stdout → Loki).

## Run

```bash
npm install
cp .env.example .env.local    # adjust service URLs for your topology
npm run dev                   # http://localhost:3000
npm run test                  # vitest (RBAC matrix, token verification, panels)
npm run build && npm start    # production build
```

The backend comes up per docs/RUNBOOK.md §2 (`make dev` + `go run ./cmd/identity …`);
host-run defaults here match the RUNBOOK port map (8081–8086).

## Layout

```
src/proxy.ts                  deny-by-default route gate (Next 16 middleware)
src/lib/auth/                 staff allowlist, JWT/JWKS verify, session cookies, guards
src/lib/api/                  server-side upstream clients (jobs, dispatch, health, problem+json)
src/lib/escalations.ts        escalation rules + queue builder (pure, unit-tested)
src/lib/pii/mask.ts           identifier masking (PII-HANDLING.md)
src/lib/audit/audit.ts        structured audit events (auth + console views)
src/components/panels/        the four panels (plain components, server-fed props)
tests/                        vitest: RBAC matrix, token verify, guard, masking, panels
```
