# Motivra Drive — customer web app (apps/customer)

Motivra Drive is the customer surface of Motivra: service requests, roadside
assistance, the **Vehicle Passport** and verified vehicle history. This
directory is the **web foundation** (issue #24): a Next.js 15 App Router
application with the landing narrative, a login form shaped to the identity
contract, a dashboard shell, and a typed API client scaffold derived from the
OpenAPI contracts.

> The garage comes to you — and the vehicle never forgets.

## Stack

- Next.js 15 (App Router) + TypeScript (strict)
- Tailwind CSS 4
- Vitest + @testing-library/react (jsdom)
- ESLint (next/core-web-vitals + next/typescript)

All dependency versions are pinned exactly in `package.json`.

## Run it

```bash
npm install
npm run dev        # http://localhost:3000
npm run lint
npm test           # vitest run
npm run build      # production build (must exit 0)
```

Requires Node 20+ (built and tested on Node 24).

## Environment variables

| Variable | Required | Default | Purpose |
| --- | --- | --- | --- |
| `NEXT_PUBLIC_API_BASE_URL` | no | `""` (same-origin) | Base URL of the Motivra API gateway. Both contracts declare a same-origin deployment behind the platform gateway, so the empty default is the contract-conformant choice. Client code: `src/lib/api/client.ts` (`apiBaseUrl()`). |

Copy `.env.example` to `.env.local` for local overrides. No secrets belong in
`NEXT_PUBLIC_*` variables — they are inlined into the client bundle.

## Routes

| Route | What it is today |
| --- | --- |
| `/` | Landing page with the real Motivra narrative (problem, nine product surfaces, MVP loop, audiences, honest status). |
| `/login` | Sign-in form shaped to `POST /v1/auth/login` (`LoginRequest { email, password }`), client-side validation, explicit "API integration pending" state on valid submit. No network calls. |
| `/dashboard` | App shell (sidebar: Dashboard / Vehicles / Jobs / Passport / Settings + topbar) with empty-state cards naming the contract endpoint each area will consume. |
| `/dashboard/vehicles` | Vehicle registry table wired to `GET /v1/vehicles` at **runtime** — loading, error (with retry), empty and data states, plus cursor-aware "Load more". Rows deep-link to the passport route. |
| `/dashboard/vehicles/[vehicleId]` | Per-vehicle passport timeline placeholder component (`sample` route available). |

## API client (`src/lib/api`)

Types in `src/lib/api/types.ts` are **hand-derived** — field for field — from:

- [`contracts/identity/openapi.yaml`](../../contracts/identity/openapi.yaml) — auth + users
- [`contracts/vehicles/openapi.yaml`](../../contracts/vehicles/openapi.yaml) — registry, passport, history

Endpoint functions (`login`, `register`, `refresh`, `logout`, `getProfile`,
`listVehicles`, `registerVehicle`, `getVehicle`, `getVehiclePassport`,
`listVehicleHistory`, `recordVehicleMileage`) call those paths verbatim.
Endpoint maps (`IDENTITY_ENDPOINTS`, `VEHICLE_ENDPOINTS`) double as UI copy so
the contract path is visible in the interface. When a contract changes, update
the types in the same PR.

`listVehicles` walks the keyset: pass one page's `next_cursor` back as
`cursor`. Note that `src/lib/api/auth-token.ts` is the **interim** token
source for runtime calls: it reads `localStorage["motivra.access_token"]`
until the identity wiring lands. With no token present the request still goes
out and the API's 401 surfaces in the UI's error state — never a fake success.

## Real vs stubbed (honesty table)

No incomplete features, no fake data — the repository policy (no mocked
production flows) applies to this app.

| Surface | Status | Consumes (contract path) |
| --- | --- | --- |
| Landing copy & product table | **Real** (condensed from the repo README) | — |
| Design system, a11y foundations | **Real** (landmarks, labeled controls, focus-visible, reduced-motion, contrast) | — |
| Login form + client-side validation | **Real UI** — submit shows explicit pending state | `POST /v1/auth/login` — integration pending |
| Dashboard shell & navigation | **Real UI** | — |
| Dashboard cards | **Real UI**, empty-state only | `GET /v1/jobs`, `GET /v1/vehicles/{vehicleID}/passport`, `POST /v1/dispatch/score`, `GET /v1/users/me` — pending. (The Vehicles card is wired: see the next row.) |
| Vehicle table | **Contract-wired** — `GET /v1/vehicles` called at runtime from the browser (loading / error+retry / empty states, keyset "Load more"). Needs the API reachable **and** a Bearer token; without a token the API's 401 renders as the error state. Still **no data at build time** — the production build performs no network calls. Interim token source: `localStorage["motivra.access_token"]` (see `src/lib/api/auth-token.ts`) until identity wiring lands. | `GET /v1/vehicles` (limit + cursor keyset) |
| Passport timeline | **Placeholder component** (event types per contract) | `GET /v1/vehicles/{vehicleID}/passport`, `GET /v1/vehicles/{vehicleID}/history` — pending |
| Typed API client | **Real** — `login`…`recordVehicleMileage` incl. `listVehicles`; the vehicle list uses it at runtime, the remaining surfaces are wired but not yet called by any page | all of the above |
| Jobs / Passport / Settings pages | **Stubbed** — disabled nav entries with "soon" badge, no fake screens | — |

## Accessibility (issue #24 acceptance)

- Semantic landmarks: `header`, `nav` (labeled), `main`, `aside`, `footer`
- Every input has a `<label>`; errors use `role="alert"` + `aria-invalid` + `aria-describedby`
- Skip-to-content link on every page; `aria-current="page"` on active nav items
- Global `:focus-visible` outline (high-contrast amber, keyboard-only)
- `prefers-reduced-motion` respected globally (animations/transitions collapsed)
- Contrast: body text ≥ 7:1, links/buttons ≥ 4.5:1 against their backgrounds

## Testing

```bash
npm test                # vitest run (jsdom)
```

Coverage today: landing renders hero + narrative sections + repo link +
product table; dashboard shell renders nav, honest badges and main landmark;
login form renders labeled fields, validates and shows the pending state
without network calls (`fetch` is asserted to never be called); vehicle list
mocks the fetch layer and covers (a) rows + passport deep-links from a mocked
`VehicleList` response, (b) the empty state, (c) the error state with a working
retry (plus the 401 hint), and (d) "Load more" appending page two via
`next_cursor`.

## Ownership

`apps/customer/**` is owned by the Motivra Drive owner (see
`docs/ARCHITECTURE.md` §2). Do not edit outside the zone.
