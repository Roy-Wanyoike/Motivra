# Product spec — Graduate-mechanic employment pipeline ("no garage required" technician track)

Status: **Phase 1 delivered** (this document, the draft API contract and the Tech app
screens). Backend phases are filed as child issues — see §6.
Issue: [#126](https://github.com/Roy-Wanyoike/Motivra/issues/126) · Contract:
[`contracts/technician/openapi.yaml`](../../contracts/technician/openapi.yaml) (draft) ·
App: [`apps/technician`](../../apps/technician).

## 1. Problem

Kenya (and African markets broadly) produces large numbers of qualified mechanic
graduates who **do not own garages**. The traditional employment path is gated on asset
ownership: without a garage, tools, or a landlord, a skilled graduate cannot reach
customers, build a reputation, or earn from their trade. Motivra's premise — *"the
garage comes to you"* — already decouples the work from the workshop, but until now the
platform had no structured pipeline that turns a graduate mechanic into an earning,
verified, dispatched technician.

This pipeline operationalizes the core promise for the demographic that needs it most.
It is also the supply engine of the marketplace: every verified graduate expands the
fulfillment capacity of the entire loop (more technicians → better coverage/ETA → more
service requests → more earnings → more technicians), and it is the employment-
generation story of the platform: **income without requiring capital ownership**.

## 2. Personas

| Persona | Situation | What this pipeline must give them |
| --- | --- | --- |
| **The graduate** (primary) | Finished a mechanical trade course (NITA/NYS/polytechnic), no garage, partial tool kit, phone is their only office, intermittent connectivity, M-Pesa is their bank | A guided path: join → prove skill → get dispatched → see earnings → get paid to M-Pesa, without owning or renting anything |
| **The senior verifier** (operator) | Experienced technician/inspector administering skills assessments; often in the field with poor connectivity on the admin side too | A trusted way to record assessment outcomes once, auditably, against a standard rubric — and never have a graduate self-grant |
| **The customer** | Vehicle owner requesting service | Confidence that whoever arrives is verified and skilled for the job — even when the cheapest supply is a new graduate |
| **Motivra ops (Command)** | Internal operations staff | Oversight of the pipeline: pending verifications, active verified technicians, earnings stats (#123) |
| **The investor** (narrative audience) | Evaluating market-fit and impact | Verifiable employment-generation metrics: graduates onboarded → verified → earning |

## 3. Journey map

The end-to-end journey, with every stage's actor, preconditions, system touchpoints,
failure paths, PII and audit obligations:

```text
sign-up → skills profile → skills assessment (Inspect template)
        → verification credential → dispatch eligibility → job earnings → M-Pesa payout
```

### Stage 1 — Sign-up

| Aspect | Detail |
| --- | --- |
| Actor | Graduate mechanic (self-serve) |
| Preconditions | A phone; an email or phone number they control |
| System touchpoints | **identity** (`POST /v1/auth/register`, TECHNICIAN role grant); **Tech app** onboarding wizard step 1 (sign-up context, offline-safe) |
| Behaviour | The account is created in the identity context (roles/RBAC, argon2id credentials, breach checks). The Tech app runs offline-first: the wizard explains what sign-up does and continues collecting the skills profile locally when the network is absent (§5 Phase-1 honesty) |
| Failure paths | Duplicate email/phone → 409 from identity; breached password → 422; offline → wizard proceeds on-device, sign-up completes when connectivity returns (outbox pattern) |
| PII touched | `users` class: email, phone, full_name (identity 000001) — see PII-HANDLING.md inventory. Passwords are credential material, never retrievable |
| Audit | `identity.user.registered` (existing fail-closed audit) |

### Stage 2 — Skills profile

| Aspect | Detail |
| --- | --- |
| Actor | Graduate mechanic (self-serve) |
| Preconditions | Account exists (or will exist); the wizard works offline |
| System touchpoints | **Tech app** onboarding wizard step 2 (skills profile form: trade, specializations, tools owned, coarse service area); **identity** `POST /v1/technician/profile` + `GET /v1/technician/me` (Phase 2, [#166](https://github.com/Roy-Wanyoike/Motivra/issues/166)); contract: `contracts/technician/openapi.yaml` |
| Behaviour | The technician declares a primary trade, up to 6 specializations, tools they own (advisory, never a gate — "no garage required" expects partial kits) and a **coarse** service area (area + county). Drafts persist on-device through the outbox and sync when the backend is reachable |
| Failure paths | Offline → draft persists locally, op queued in the outbox with backoff; validation failure → 422 surfaced in the wizard, op parked as FAILED with the server verdict (never retried blindly) |
| PII touched | Skills profile class (new in #166): trade/specializations/tools (not personal data), **coarse service area only** — precise or historical geolocation is never stored (PII-HANDLING.md: "Not stored anywhere: … precise geolocation history") |
| Audit | Profile writes are self-service (no privileged action); reads of the profile by operators are covered by the identity audit trail in #166's design |

### Stage 3 — Skills assessment (via Inspect template)

| Aspect | Detail |
| --- | --- |
| Actor | Senior verifier (operator) administers; graduate performs |
| Preconditions | Profile submitted; an assessment rubric exists as an Inspections **dynamic template** (Wave 3 machinery — reuse, don't reinvent); an assessment session is opened for the graduate |
| System touchpoints | **inspections** (`POST /v1/inspections/sessions`, findings, evidence, report — template = assessment rubric); **Tech app** shows "assessment pending"; **Command** (#123) verification queue for ops oversight |
| Behaviour | The verifier runs the assessment session against the rubric with evidence capture (photo provenance as in any inspection). Opening the session moves the graduate's status to `assessment_pending` |
| Failure paths | Assessment session abandoned → technician stays `assessment_pending` until closed; template/session not found → 404; evidence upload interrupted → resumable uploads per the offline-first rule (ARCHITECTURE.md §7) |
| PII touched | Assessment media (photos of work, potentially faces/ID documents in frame) — **sensitive PII**: signed-URL access only, retention policy per PII-HANDLING.md; findings reference evidence by id. This is the pipeline's highest-sensitivity surface |
| Audit | Session lifecycle transitions (append-only, existing inspections machinery); the eventual outcome write is audited in Stage 4 |

### Stage 4 — Verification credential

| Aspect | Detail |
| --- | --- |
| Actor | Senior verifier (operator) — NEVER the graduate (no self-serve privilege escalation) |
| Preconditions | Assessment session completed; verifier role authenticated (role set finalized in #166 — dedicated VERIFIER or ADMIN, decided there) |
| System touchpoints | **identity** `POST /v1/technician/verification` (operator submits outcome; audit-logged), `GET /v1/technician/verification-status`; **Tech app** profile screen shows unverified / assessment-pending / verified with an honest explanation of what the credential means; **Command** pending-verifications queue |
| Behaviour | `pass` writes an append-only verification record (who, when, against which template/session) **and** the audit entry in one transaction — a failed audit write fails the request (ADR-0004 fail-closed). `fail` records the attempt, status stays `unverified`. Revocation is a new superseding record; rows are never mutated. The credential is tamper-evident (hash-chained like other integrity surfaces) |
| Failure paths | Non-verifier caller → 403 (including the graduate attempting self-grant — test-covered in #166); granting over a live credential → 409; audit write failure → whole request fails, nothing half-recorded |
| PII touched | Verification record class (new in #166): verifier/technician ids (references), verifier notes (free text, redacted through `platform.RedactFreeText` before persistence); assessment evidence stays in Inspections referenced by id |
| Audit | `identity.technician.verification.granted` / `.failed` / `.revoked` — append-only `audit_log`, who verified, when, against which template/session (mirrors #126's acceptance criterion) |

### Stage 5 — Dispatch eligibility

| Aspect | Detail |
| --- | --- |
| Actor | Dispatch engine (system); dispatcher role for manual assignment |
| Preconditions | Verification status readable by dispatch (Phase 2 output); job in DISPATCHING |
| System touchpoints | **dispatch** eligibility filter (verified-only + skills + service-area matching — [#167](https://github.com/Roy-Wanyoike/Motivra/issues/167)); **jobs** assignment; **Command** live dispatch map |
| Behaviour | Only `verified` technicians are eligible for assignment, enforced server-side **before** scoring. Skills (specializations vs. job needs) and coarse service area narrow the candidate set. Eligibility verdicts land in the decision audit metadata — explainable by design (repo principle 12) |
| Failure paths | Unverified technician proposed → ineligible (negative test required by #126); no eligible technician → job escalates through the existing jobs state machine; stale verification (expiry/revocation) → eligibility re-checked per decision |
| PII touched | None new — coarse areas and skill enums only; technician precise location is never consumed (PII-HANDLING.md) |
| Audit | Dispatch decision records already carry the decision inputs hash, chosen technician and scores; #167 adds the eligibility exclusion reasons to the same audit surface |

### Stage 6 — Job earnings

| Aspect | Detail |
| --- | --- |
| Actor | Graduate (now a verified, dispatched technician); customer pays |
| Preconditions | Job COMPLETED; payment captured (payments context, M-Pesa-first) |
| System touchpoints | **payments** ledger (authoritative, integer minor units); **Tech app** earnings screen via `GET /v1/technician/earnings` ([#168](https://github.com/Roy-Wanyoike/Motivra/issues/168)); **Command** earnings stats |
| Behaviour | Each completed job produces a ledger-backed earnings line: gross → platform fee → net (the technician always sees the split — README revenue model), in integer KES cents. The Tech app reads; it never computes money |
| Failure paths | Ledger unavailable → earnings screen shows the degraded "sync pending" state honestly; discrepancy → reconciliation job (payments `reconcile.go`), never client-side adjustment |
| PII touched | Money references only (job/intent/transaction ids); no new PII |
| Audit | Ledger rows and payment state transitions are append-only with idempotency keys (repo principle 4) |

### Stage 7 — M-Pesa payout

| Aspect | Detail |
| --- | --- |
| Actor | Payments system; graduate receives |
| Preconditions | Positive `ledgered` balance; payout policy decided (manual request vs. scheduled — owner decision in #168); M-Pesa B2C credentials provisioned (owner input — blocker noted in #168) |
| System touchpoints | **payments** provider abstraction (Daraja M-Pesa B2C) with idempotency keys, append-only payout transactions, reconciliation; **Tech app** payout state; **Command** pending-payouts oversight |
| Behaviour | Balance pays out to the phone number already held in identity (no new PII). Payout states: PENDING → SENT → SETTLED / FAILED, each transition audited and reconcilable |
| Failure paths | Provider timeout → retry + reconcile (external failure is a designed case, ARCHITECTURE.md §7); double-submit with the same idempotency key cannot double-pay (test-covered in #168); FAILED payout surfaces honestly in the app and Command |
| PII touched | `users.phone` reused (existing class); M-Pesa callbacks logged redacted (PII-HANDLING.md request-log rules) |
| Audit | Payout transactions append-only with provider references; reconciliation reports |

## 4. What "verified" means (the honest promise)

A **Verified Technician** credential means: a Motivra verifier-role operator assessed
this technician against the published skills rubric (an Inspections template) and
recorded a `pass` outcome — with the who/when/template/session attribution preserved
tamper-evidently. It does **not** claim years of experience, warranty of any specific
repair, or licensing status. The Tech app states this in plain language on the profile
screen so graduates and customers share the same expectation.

## 5. Phase plan

| Phase | Scope | Delivery |
| --- | --- | --- |
| **Phase 1 (this PR)** | Journey spec (this doc) + draft API contract (`contracts/technician/openapi.yaml`) + Tech app screens (onboarding wizard, profile, earnings) running in honest degraded mode — no Go changes (conflict-safe parallelism with the degraded-boot wave) | #126 Phase 1 |
| **Phase 2 — identity verification-status backend** | `technician_profiles` + append-only `technician_verifications`, verifier-role API, audit-logged outcomes, `verification-status` read; contract moves draft → implemented | [#166](https://github.com/Roy-Wanyoike/Motivra/issues/166) (child issue; sequenced **after** the #122 degraded-boot merges to avoid `cmd/identity` conflicts) |
| **Phase 3 — dispatch eligibility filter** | Verified-only eligibility enforced server-side before scoring + skills + service-area matching, exclusion reasons in the decision audit | [#167](https://github.com/Roy-Wanyoike/Motivra/issues/167) (child issue; depends on #166) |
| **Phase 4 — payouts** | Ledger-backed `GET /v1/technician/earnings`, M-Pesa B2C payout via the provider abstraction, reconciliation, Tech app live earnings/payout states | [#168](https://github.com/Roy-Wanyoike/Motivra/issues/168) (child issue; builds on the merged Wave 5 payments foundation; needs owner input: M-Pesa B2C credentials + payout policy) |

Ops oversight of the whole pipeline rides on Command (#123): pending verifications
(Phase 2), active verified technicians (Phase 3), earnings stats + pending payouts
(Phase 4).

## 6. Issue #126 acceptance criteria → phase mapping

| #126 acceptance criterion | Phase | Where it is enforced / proven |
| --- | --- | --- |
| A graduate can complete onboarding end-to-end on the Tech app and become dispatch-eligible after assessment | 1 (screens) + 2 (#166 identity backend) + 3 (#167 eligibility) | Phase 1 ships the wizard + profile with offline drafts; #166 makes the outcome server-authoritative; #167 flips dispatch eligibility |
| Unverified technicians receive NO dispatched jobs (enforced server-side, test-covered) | 3 | #167 — eligibility gate before scoring + negative tests |
| Earnings per job are visible in the Tech app and reconcile with the ledger | 4 | #168 — ledger-backed earnings read + reconciliation invariant tests; Phase 1 ships the honest degraded screen |
| Verification outcomes are audit-logged (who verified, when, against which template) | 2 | #166 — fail-closed audit rows in one transaction with the verification record |
| PII (ID documents, assessments) handled per docs/PII-HANDLING.md | 1 (this spec §3 PII rows) + 2 (#166 implementation) | Signed URLs + retention for assessment media; coarse-only service areas; redacted free text; PII-HANDLING.md inventory updated with every schema change (change discipline) |

## 7. Contract linkage

- `contracts/technician/openapi.yaml` (new, **draft 0.1.0**) defines the Phase 2/4
  surface: `POST /v1/technician/profile`, `GET /v1/technician/me`,
  `POST /v1/technician/verification`, `GET /v1/technician/verification-status`,
  `GET /v1/technician/earnings`.
- Spec-first: the Tech app consumes these paths behind a typed client
  (`apps/technician/src/api/technician.ts`). Phase 1 runs degraded — mutations queue
  through the existing outbox (at-most-once per idempotency key), reads are labelled
  "backend pending". No path is implemented server-side yet; #166 and #168 implement
  to match, then move the contract out of draft (producers own schemas, ADR-0002).
- Assessment rubrics reuse `contracts/inspections` templates; job/assignment state
  remains `contracts/jobs`; money stays `contracts/payments` (ledger authoritative).
