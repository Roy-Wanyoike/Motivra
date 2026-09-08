# Backend — Dispatch

Technician availability, service areas and explainable dispatch scoring (fit, distance, equipment, performance). The dispatch engineer owns this zone; see docs/ARCHITECTURE.md §2 — do not edit outside your zone.

This directory currently contains the **dispatch scoring engine** (issue #19): `scoring.go` (the engine), `http.go` (the HTTP API) and their tests. The service boots from `cmd/dispatch`.

## Philosophy

- **Never nearest-only.** Proximity is one weighted factor among six. A nearby but unqualified technician must never outrank a slightly farther, fully qualified one. The ranking test in `scoring_test.go` pins this.
- **Explainable by construction.** Every factor carries a weight, a raw metric, a normalized 0-1 value and a human-readable reason that names its inputs ("matches 2/2 required skills (engine, brakes); rated on Toyota"). There are no hidden multipliers: anything that moves a score is a named factor, and `GET /v1/dispatch/factors` publishes the full catalogue. The directive requires that a dispatcher can always ask "why this technician?" and get an answer.
- **Advisory, not autonomous.** The engine recommends an ordering. It never assigns, reassigns or cancels a job. Decisions (and their audit trail) land with the jobs integration.
- **Pure function.** No database, no broker, no clock, no randomness. The service is stateless; all inputs arrive in the request.

## Factors

Default weights sum to 1.0 so `total` stays comparable across jobs. Every normalized value is in [0, 1] and rounded to 4 decimals.

| Factor | Weight | Formula | Example reason |
|---|---|---|---|
| `technical_fit` | 0.30 | skill overlap ratio × make-match bonus (1.0 rated on make, 0.5 not rated; 1.0 when the job names no skills or no make) | "matches 1/2 required skills (engine); missing (brakes); not rated on Toyota; 0.5 make bonus applied" |
| `proximity` | 0.20 | 1/(1+ETA/30); 0 when unavailable (ETA is the half-life at 30 min). Distance is reported in the reason, the ETA drives the score | "ETA 12 min, 4.2 km away; proximity 0.71" |
| `availability` | 0.15 | 1 available, 0.5 declined the last offer, 0 unavailable | "technician is available but declined the last dispatch offer (0.5 availability penalty)" |
| `equipment` | 0.15 | 1 required tools on van, 0 otherwise | "missing required tools or equipment" |
| `parts` | 0.10 | 1 all required parts on van (or job needs none), 0.5 parts missing (partial coverage, sourced en route) | "missing required parts (brake pads, rotor); partial parts coverage scored 0.5" |
| `performance` | 0.10 | 0.4 × completion rate + 0.4 × (avg rating / 5) + 0.2 × acceptance rate | "completion 98%, rating 4.8/5, acceptance 92% over 214 completed jobs" |

Notes:

- The parts factor never reaches 0 with today's single `has_required_parts` boolean: the engine cannot distinguish "none" from "some", so missing parts earn 0.5 rather than a hard zero. A true zero arrives with per-part coverage data.
- Empty `required_skills`, `required_parts` or `vehicle_make` mean "no constraint" and never penalize a candidate.
- `top_reasons` lists factors by contribution (weight × normalized) and skips zero-contribution factors: a factor that did not move the score is not a reason.

## Determinism guarantee

The engine is a pure function of (candidate, job context, weights):

- No randomness, no wall-clock, no I/O, no goroutines.
- Every factor value and the total are rounded to 4 decimal places, so output is byte-for-byte reproducible.
- `ScoreCandidates` sorts by total descending with ties broken by ascending technician ID — stable across runs even for identical scores.
- The same request body always produces the same response body.

## HTTP API

Contract: `contracts/dispatch/openapi.yaml`.

| Endpoint | Roles | Purpose |
|---|---|---|
| `POST /v1/dispatch/score` | DISPATCHER, ADMIN, SUPER_ADMIN | Score a candidate set (cap 200) against a job context; optional custom weights (must sum to 1.0) |
| `GET /v1/dispatch/factors` | DISPATCHER, ADMIN, SUPER_ADMIN | Factor catalogue + default weights |

Input validation rejects out-of-range rates, negative ETA/distance, zero technician IDs and oversized lists rather than silently clamping, so a broken producer cannot distort a recommendation.

## Future inputs

Deliberately out of scope for the engine today, and shaped so they can slot in without changing the factor set:

- **Inventory integration** — per-part coverage replaces the single boolean, unlocking a true zero for the parts factor.
- **Historical performance service** — completion/acceptance/rating computed by the jobs domain instead of being posted by callers.
- **Live availability feed** — calendar and shift data instead of a posted flag; the declined-last penalty stays (it is a trust signal, applied openly).
- **Traffic-aware ETA** — routing service ETA replaces the posted estimate; the proximity formula does not change.
- **Tenant-tunable weights** — per-tenant weight sets, versioned and audited, instead of the shipped defaults.
- **Decision logging** — recording chosen technician, scores and reasons with the dispatch decision, when jobs integration lands.

## Do NOT

- **No opaque punishment.** No hidden multipliers, shadow penalties or factors outside the published catalogue; every effect must appear in `factors` with a reason.
- **No auto-reassignment.** The engine scores; it never mutates job state. Assignment, reassignment and cancellation belong to the jobs domain.
- **No PII in scores or reasons.** Reasons name skills, makes, parts and metrics — never customer or technician personal data.
- **No randomness or time-based inputs.** Determinism is a contract; the same input must always produce the same output and ordering.
