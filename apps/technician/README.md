# Motivra Tech (technician) — Foundation

React Native (Expo) offline-first field app. This is the **Wave 4 foundation** delivered
against [issue #29](https://github.com/Roy-Wanyoike/Motivra/issues/29), extended by the
full conflict-resolution protocol of
[issue #55](https://github.com/Roy-Wanyoike/Motivra/issues/55): the offline outbox,
the three-way-merge sync reconciler with 409-driven intent re-basing, pure job-state
guards mirroring `contracts/jobs`, and the first two screens wired through a tiny typed
navigation context.

Owned by the Motivra Tech owner; see `docs/ARCHITECTURE.md` §2 — do not edit outside
this zone.

## Why this exists

The technician app is the operational heart of the MVP loop: job acceptance, en-route,
inspection, diagnosis, estimate and repair all start on a device that may be offline in
a low-connectivity Nairobi workshop. Architecture baseline (ARCHITECTURE.md §7): *the
technician app is offline-first — local DB, outbox, sync queue, idempotency, conflict
detection, resumable uploads — field technicians never lose job data to network
failure.* This foundation implements the outbox + idempotency + backoff core; the
persistence and transport layers are stubbed honestly (table below).

## Run it

```bash
cd apps/technician
npm install
npx tsc --noEmit     # typecheck gate
npx vitest run       # unit-test gate (pure logic: outbox, backoff, guards, sync)
npx expo start       # on a machine with Android/iOS tooling or Expo Go
```

The verified merge gates in CI-less conditions (Actions billing lock, issue #10) are
`tsc --noEmit` and `vitest run`. Rendering the UI requires Expo Go or a native build —
not runnable in the delivery sandbox, documented below.

## Layout

```
apps/technician/
├── App.tsx                     # shell: navigation + sync provider + banner
├── app.json                    # Expo config ("Motivra Tech")
├── index.ts                    # registerRootComponent entrypoint
├── src/
│   ├── domain/job-state.ts     # 18-status transition guards (mirrors backend/jobs)
│   ├── outbox/                 # CORE: operation schema, storage adapter,
│   │   ├── types.ts            #   enqueue + drain, exponential backoff + jitter,
│   │   ├── backoff.ts          #   idempotent replay (at-most-once per key)
│   │   ├── storage.ts          #   InMemoryOutboxStorage (SQLite adapter = follow-up)
│   │   └── outbox.ts
│   ├── sync/                   # reconciler skeleton: push (drain) → pull (cursor)
│   │   ├── api.ts              #   conflict-detection placeholder (no last-write-wins)
│   │   ├── reconciler.ts       #   server-authoritative state handling
│   │   └── SyncContext.tsx     # React wiring for the sync banner
│   ├── navigation/NavigationContext.tsx  # tiny typed nav (expo-router upgrade path below)
│   ├── screens/                # JobListScreen, JobDetailScreen (timeline + actions)
│   ├── components/SyncBanner.tsx
│   ├── state/job-store.ts      # in-memory read model (JobLocalStore impl)
│   ├── mocks/jobs.ts           # stub data + SimulatedJobsApi dispatcher
│   └── lib/                    # uuidv4 (expo-crypto backed), JSON value types
└── tests/                      # vitest: idempotency, backoff, exhaustive guards, sync
```

## The outbox model

Every offline mutation becomes an `OutboxOperation`:

| Field | Notes |
| --- | --- |
| `id` | UUIDv4 operation id |
| `type` | e.g. `job.transition` |
| `payload` | JSON body exactly as it will be sent |
| `idempotency_key` | client-generated, sent as `Idempotency-Key`; server applies **at-most-once per key** |
| `created_at` / `next_attempt_at` | ISO-8601 (`next_attempt_at` doubles as the IN_FLIGHT lease start) |
| `attempts` | retry counter |
| `state` | `PENDING → IN_FLIGHT → DONE`, plus `FAILED` (permanent 4xx or dead-lettered) |

Guarantees implemented and tested:

1. **Enqueue-time dedupe** — the same logical action enqueued twice with one key yields
   one operation (`deduped: true`).
2. **Idempotent replay** — a crash between server-apply and the local `DONE` write is
   recovered by lease reclaim; the replay re-sends the *same* key and the server applies
   it once. Test: `tests/outbox.idempotency.test.ts` ("CRASH REPLAY").
3. **Backoff + jitter** — `min(maxDelay, base·2^n)` with symmetric uniform jitter
   (default ±25%, 1s → 5min cap, dead-letter after 8 attempts). Test:
   `tests/outbox.backoff.test.ts`.

**Honest scope:** the server-side half of idempotency (a real `Idempotency-Key`
middleware in the jobs service) does not exist yet; the tests prove the client-side
contract against a simulated server that implements it.

## Sync reconciler

`SyncReconciler.sync()` = **push** (drain outbox) → **re-base** (settle intents whose
ops reached DONE/FAILED) → **pull** (cursor-paged server deltas through the three-way
merge). The full conflict protocol (issue #55, `src/sync/merge.ts` + `src/sync/api.ts`
→ `mergeIncoming`) — all rules enforced by `tests/sync.conflict.test.ts`:

- **Server-authoritative fields**: once adopted, `status`, `technician_id`, timestamps,
  prices, payment/inspection state, vehicle history and technician status are never
  taken from the client. A live intent may only overlay `status`.
- **Three-way merge (base / client-intent / server-state)**: drift detection compares
  the pulled snapshot to the last synced base (`serverBaseForJob`); a live intent
  survives a server move only if the proposed transition is still legal from the
  server's new state (pure guard mirror).
- **No last-write-wins**: a contradicting server state adopts the server snapshot AND
  attaches an unacknowledged `local_conflict` to the display — visible in the job
  detail screen until the technician acknowledges it (`clearConflict`).
- **409-driven intent re-basing**: ops the server permanently rejects become
  persistent `rejectedIntents()` records (deduped per op id); the optimistic overlay
  is dropped and the verdict is re-presented — never silently overwritten.
- **Intent already applied** (server status moved to the proposed target): server
  snapshot wins outright, overlay dropped.
- **Append-only data**: transition rows are insert-only; the merge carries them
  through untouched, so conflicts on history are impossible by construction.
- Pull still targets a future `GET /v1/jobs/sync?cursor=` endpoint that the jobs
  contract does not expose yet (tracked contracts gap); tests inject the fake.

## Job state machine

`src/domain/job-state.ts` mirrors `backend/jobs/statemachine.go` and
`contracts/jobs/openapi.yaml` (18 statuses; COMPLETED/CANCELLED/FAILED terminal;
ESCALATED a dead end). Screens call `guard(next, current)` **before** enqueueing a
`job.transition` op, so illegal moves never leave the device. The exhaustive 18×18
matrix test pins the guard to the contract edge list restated independently.

## Environment variables

| Variable | Purpose | Default |
| --- | --- | --- |
| `EXPO_PUBLIC_MOTIVRA_API_URL` | Base URL of the jobs API (planned real transport) | unset → simulated API |
| `EXPO_PUBLIC_MOTIVRA_ACCESS_TOKEN` | Bearer token for local manual testing only | unset |

No secrets are committed; the real auth flow (identity service HS256 tokens, refresh
rotation) is a follow-up.

## Honesty table — real vs stubbed

| Area | Status in this PR | What's real | What's stubbed / follow-up |
| --- | --- | --- | --- |
| Outbox core (schema, enqueue, drain, backoff+jitter, idempotent replay) | ✅ Real, unit-tested | All logic in `src/outbox` | — |
| Outbox persistence | 🟡 Stub | Adapter interface + in-memory fake | expo-sqlite adapter (durable across restarts) |
| Sync push (drain) | ✅ Real, unit-tested | Drives the outbox | — |
| Sync pull | 🟡 Merge protocol real, transport pending | Push → re-base → pull; three-way merge + 409 intent re-basing (issue #55, 17 rule tests) | Server has no delta endpoint yet (`contracts/jobs` change needed); app seeds from mocks |
| Job state guards | ✅ Real, exhaustively tested | 18×18 matrix vs contract | Server remains authoritative; guard is predictive only |
| Transport (fetch → jobs API) | 🔴 Stubbed | `OutboxDispatcher`/`PullFn` interfaces; contract-shaped payloads | `SimulatedJobsApi` accepts everything; real fetch client + error mapping is follow-up |
| Screens (list, detail, timeline, actions) | 🟡 Real code, untested visually | RN components typed under `strict` | No rendering/E2E tests (needs RN test renderer / Maestro) |
| Navigation | 🟡 Real, tiny | Typed route union + stack | expo-router migration documented below |
| Connectivity detection | 🔴 Stubbed | Banner reflects last sync outcome | `@react-native-community/netinfo` follow-up |
| UUIDv4 | ✅ Real | `expo-crypto` `randomUUID` (SDK 57 pin), Web Crypto fallbacks, no weak-randomness path | Node test runtime uses an aliased pure double (`tests/stubs/expo-crypto.ts`) |
| Auth | 🔴 Not started | — | identity service integration (Wave 1 output exists server-side) |

## Upgrade path to expo-router

Navigation is isolated to `src/navigation/NavigationContext.tsx` and two hooks.
To migrate: `npx expo install expo-router react-native-safe-area-context react-native-screens expo-linking expo-constants expo-status-bar`; move `JobListScreen` to
`app/index.tsx` and `JobDetailScreen` to `app/job/[id].tsx`; replace
`navigate({name:'JobDetail', params:{jobId}})` with `router.push(`/job/${id}`)`. The
`Route` union already matches those file shapes, so screen logic is unchanged.

## EAS build (when tooling exists)

```bash
npm install -g eas-cli          # on a machine with Android/iOS tooling
eas login
eas build:configure             # generates eas.json (commit it)
eas build --platform android --profile preview   # APK for field pilots
eas build --platform ios --profile preview       # requires Apple Developer account
eas submit --platform android   # Play Console after credentials are set up
```

No Android/iOS toolchain existed in the delivery sandbox, so **no native build
evidence is included in the foundation PR** — `tsc --noEmit` + `vitest run` are the
verified gates. `expo start` smoke-testing and an EAS dry-run are the first post-merge
actions on a provisioned machine.

## Contract linkage

Job shapes, statuses and transition payloads come from
[`contracts/jobs/openapi.yaml`](../../../contracts/jobs/openapi.yaml) (`JobStatus`,
`Job`, `JobTransition`, `TransitionRequest`). Any contract change that affects this app
must be negotiated with the jobs context owners — producers own the schemas (ADR-0002).
