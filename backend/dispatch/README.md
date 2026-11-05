# Backend — Dispatch

Technician availability, service areas and explainable dispatch scoring (fit, distance, equipment, performance). See docs/ARCHITECTURE.md §2 for the context boundaries.

This directory currently contains the **dispatch scoring engine** (issue #19) and the **verified-only eligibility gate** (issue #167): `scoring.go` (the engine), `eligibility.go` (the gate), `http.go` (the HTTP API), `audit.go` (decision records) and their tests. The service boots from `cmd/dispatch`.

## Philosophy

- **Never nearest-only.** Proximity is one weighted factor among six. A nearby but unqualified technician must never outrank a slightly farther, fully qualified one. The ranking test in `scoring_test.go` pins this.
- **Explainable by construction.** Every factor carries a weight, a raw metric, a normalized 0-1 value and a human-readable reason that names its inputs ("matches 2/2 required skills (engine, brakes); rated on Toyota"). There are no hidden multipliers: anything that moves a score is a named factor, and `GET /v1/dispatch/factors` publishes the full catalogue. a dispatcher can always ask "why this technician?" and get an answer.
- **Advisory, not autonomous.** The engine recommends an ordering. It never assigns, reassigns or cancels a job. Decisions (and their audit trail) land with the jobs integration.
- **Pure function.** No database, no broker, no clock, no randomness. The service is stateless; all inputs arrive in the request.

## Eligibility gate (issue #167, Phase 3 of the graduate-mechanic pipeline #126)

Before scoring, a request MAY enable up to three **hard gates** via the optional `eligibility` block on `POST /v1/dispatch/score`:

| Gate | Option | Rule |
|---|---|---|
| Verification | `verified_only` | Only technicians whose identity verification status is exactly `verified` are eligible (issue #126: unverified technicians receive NO dispatched jobs). A missing status fails closed — it is treated as not verified. Any other value (including values the identity contract may add later) is ineligible. |
| Skills | `require_skills` | The candidate's skills must intersect the job's required skills (at least one match). A job that names no skills imposes no constraint. |
| Service area | `require_service_area` | The candidate must serve the job's coarse `service_area` label (case-insensitive string match). A job with no area imposes no constraint. |

Design decisions (also recorded in the decision audit and the PR):

- **Hard gates before scoring, not weighted factors.** A weight could still let an unverified technician top the ranking when peers are scarce; exclusion is the only mechanism that satisfies "unverified technicians receive no dispatched jobs". The gate verdicts are explainable like the factors: every evaluated candidate gets a verdict with raw facts (`verified`, `skills_match`, `area_match`) and one human-readable reason per enabled gate, in the fixed order verification → skills → area.
- **Excluded candidates are never scored** ("scored-and-then-demoted" is explicitly rejected): they appear in `eligibility.verdicts` with their exclusion reasons, and never in `scores`. The decision record's chosen technician is the top **eligible** score; when nobody is eligible the decision is still audited — chosen technician is the zero UUID and the verdicts explain why nobody was recommended.
- **Skills = intersection semantics.** One matching skill passes the gate (the issue asks for an intersection); partial coverage is still down-weighted openly by the `technical_fit` factor ("matches 1/2 required skills; missing (brakes)"). All-or-nothing matching would double-gate the same data with no added safety.
- **The eligibility options are part of the inputs hash**, so a re-score under different gates is never conflated with the original decision.
- **Backward compatible:** an omitted `eligibility` block keeps the pre-#167 behaviour exactly (every candidate scored, no eligibility key in the response or the audit metadata). The block is additive in `contracts/dispatch/openapi.yaml`.

### Integration point with identity (issue #166, parallel)

The verification-status vocabulary (`unverified | assessment_pending | verified`) and the skills profile (trade, specializations) and coarse service area (`area_name` + `county`) are owned by the technician contract (`contracts/technician/openapi.yaml`, issue #166). Dispatch deliberately does **not** re-validate the status enum and does **not** read identity's tables: the caller (the dispatch console, later the jobs integration) composes candidates from #166's read APIs — `GET /v1/technician/verification-status` and the technician profile — and posts `verification_status` / `skills` / `service_areas` per candidate. Because the gate only needs "`verified` means verified", it cannot break when #166 extends its lifecycle (e.g. a rejected state). An event-driven projection (`technician.verified.v1`) is deliberately deferred; the first dispatch-chain migration will arrive with it, not before — the gate itself is stateless and adds no tables.

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
| `POST /v1/dispatch/score` | DISPATCHER, ADMIN, SUPER_ADMIN | Score a candidate set (cap 200) against a job context; optional custom weights (must sum to 1.0); optional `eligibility` block enabling the verified-only / skills / service-area gates (issue #167) |
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

- **No opaque punishment.** No hidden multipliers, shadow penalties or factors outside the published catalogue; every effect must appear in `factors` with a reason. The eligibility gate is not an exception: exclusions are hard, visible and reasoned in `eligibility.verdicts` (issue #167).
- **No auto-reassignment.** The engine scores; it never mutates job state. Assignment, reassignment and cancellation belong to the jobs domain.
- **No PII in scores, reasons or verdicts.** Reasons name skills, makes, parts, areas, statuses and metrics — never customer or technician personal data. Service areas are coarse labels only (PII-HANDLING.md).
- **No randomness or time-based inputs.** Determinism is a contract; the same input must always produce the same output and ordering — eligibility verdicts included.
