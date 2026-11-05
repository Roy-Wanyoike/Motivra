# Motivra backlog coverage & traceability matrix

**Governing issue:** [#154](https://github.com/Roy-Wanyoike/Motivra/issues/154) — Motivra full-history backlog coverage and traceability matrix.

This file is the first reviewed revision of the matrix demanded by #154. It covers:

1. the **merged delivery history** — every merged wave mapped to its PRs and the issues those PRs delivered (§1–§2);
2. the **open-issue inventory by area** (§3);
3. factual governance observations that affect traceability (§4).

The full requirement-level disposition matrix (unique requirement IDs, per-item shipped/partial/deferred/rejected classification, customer-journey and non-functional coverage, roadmap update) remains **open under #154** — this file is the factual skeleton that audit extends, not its completion.

## Method & verification

- Data read from the live repository on **2026-11-01**: `gh pr list --state merged` (**79 merged PRs**), `gh issue list --state open --limit 100` (**54 open issues**), plus per-PR body inspection for `Closes #N` / issue references.
- Every PR and issue number in this file was read from GitHub. **No reference is invented.** Where a PR "closes" an issue, the wording is taken from the PR body/title.
- Note on numbering: the wave rows in §1 use the delivery-wave grouping adopted for this audit. [`docs/ROADMAP.md`](ROADMAP.md) remains the canonical wave numbering; the mapping column ties each row back to it.

## 1. Merged delivery waves → PRs and issues

| Delivery wave | Scope | ROADMAP.md mapping | Merged PRs (issues closed via merge) | Open follow-ons |
| --- | --- | --- | --- | --- |
| **Wave 0 — Foundation** | Architecture reference, ADRs 0001–0005, monorepo skeleton, CI quality gates, engineering docs | Wave 0 — Discovery & architecture (DONE) | #1, #8 (→ #2), #9 (→ #3) — merged 2026-09-08 | — |
| **Wave 1 — Platform + identity + vehicles** | Shared platform primitives; AuthN/RBAC/audit; vehicle registry, append-only history, Vehicle Passport; tenant-scoped vehicle listing | Wave 1 — Foundation (DONE) + Wave 2 — Vehicle foundation (CORE MERGED) | #14 (platform checkpoint), #15 (→ #4), #16 (→ #5), #17 (→ #6), #46 (→ #38), #48 (wires #38 into web) — 2026-09-08/09 | — |
| **Wave 2 — Jobs + dispatch (+ field mobile)** | Service-request/job state machine; explainable dispatch scoring; technician app foundation; full offline conflict protocol | Wave 4 — Mobile garage (backend core + mobile foundation merged) | #20 (→ #19), #21 (→ #18), #43 (→ #29), #59 (→ #55) — 2026-09-08/09 | — |
| **Wave 3 — Inspections** | Template registry, session state machine, evidence provenance, verified reports; OpenAPI contract, migrations | Wave 3 — Inspection (BACKEND CORE MERGED) | #102 (→ #61) — 2026-10-14 | #125 (Inspect console), #179 (evidence storage + offline capture), #184 (template authoring lifecycle), #185 (report verification endpoint), #186 (short-TTL signed media URLs) |
| **Wave 4 — Garages + consoles + gateway** | Motivra Command MVP (staff ops console); Garage console MVP (appointments, capacity, job board, invoices); staff-console production routing; garage capacity API; API gateway (ADR-0009) | Wave 4 surface (#124) + Wave 6 Network scope (#170) + ADR-0009 gateway (#202) | #129 (serves #123 MVP), #188 (→ #124), #200 (→ #195), #203 (→ #170), #204 (→ #202) — 2026-10-21/28 | #123 (MVP live via #129; open for deferred scope), #128 (incidents read model — #123 deferred scope), #165 (garage-scoped job list), #169 (appointments API), #170 (capacity API merged via #203; issue open — see §4), #202 (gateway merged via #204; issue open — see §4) |
| **Graduate-mechanic track (#126)** | "No garage required" technician employment pipeline — phase 1: journey spec, phase-2 API contracts, onboarding/profile/earnings screens | Product track outside ROADMAP wave numbering | #189 (→ #126) — 2026-10-28 | #166 (verification API, phase 2), #167 (verified-only dispatch, phase 3), #168 (payout readiness, phase 4) |

## 2. Full merged-PR inventory (all 79 merged PRs as of 2026-11-01, grouped)

Every merged PR appears exactly once. `→ #N` = the PR body/title states it closes or delivers that issue.

| Group | Merged PRs | Count |
| --- | --- | --- |
| Wave 0 — foundation | #1, #8 (→ #2), #9 (→ #3) | 3 |
| Wave 1 — platform + identity + vehicles | #14, #15 (→ #4), #16 (→ #5), #17 (→ #6), #46 (→ #38), #48 (wires #38) | 6 |
| Wave 2 — jobs + dispatch + field mobile | #20 (→ #19), #21 (→ #18), #43 (→ #29), #59 (→ #55) | 4 |
| Wave 3 — inspections | #102 (→ #61) | 1 |
| Wave 4 — garages + consoles + gateway | #129 (#123 MVP), #188 (→ #124), #200 (#195), #203 (→ #170), #204 (#202, ADR-0009) | 5 |
| Graduate-mechanic track | #189 (→ #126) | 1 |
| Customer web (Drive) foundation | #35 (→ #24), #74 (unblocked Vercel deploys, → #72) | 2 |
| Payments (ROADMAP Wave 5) | #109 (→ #62), #112 (stack wiring + ADR-0007 env contract) | 2 |
| Production hardening & readiness | #47, #49 (→ #28), #89 (→ #80), #90 (→ #81), #91 (→ #82), #92 (→ #83), #93 (→ #84), #98 (→ #86), #110 (→ #87), #111 (→ #88), #120 (→ #118, #119), #127 (→ #122), #198 (#122 degraded boot) | 13 |
| Governance, QA & documentation | #23 (→ #22), #32 (→ #7), #33 (→ #26, #27), #34 (→ #25, #31), #37 (→ #30), #39, #51 (→ #50), #52, #56 (→ #54), #60, #75 (→ #73), #94 (→ #85), #97 (→ #96), #114 (→ #113), #197 (#187 two-branch model) | 15 |
| Vercel edge & deploy fixes | #71, #100 (→ #99), #101 (→ #99) | 3 |
| Dependency automation | #36, #40, #41, #45, #58 (→ #57), #76, #77, #79 (supersedes #63–#70), #103, #104, #105, #106, #115, #116, #117, #190, #191, #192, #193, #194 | 20 |
| Releases & branch model | #199 (dev→main release, Refs #121/#122), #201 (dev→main release), #205 (dev→main release), #206 (dev↔main sync) | 4 |
| **Total** | | **79** |

## 3. Open-issue inventory by area (54 open issues as of 2026-11-01)

Built from `gh issue list --state open --limit 100`. Each open issue appears exactly once.

| Area | Open issues | Count | Notes |
| --- | --- | --- | --- |
| Governance & audit | #10, #154 | 2 | #10 = GitHub Actions billing lock (blocks required status checks and CI workflow additions); #154 = this audit |
| Platform & infrastructure | #143, #155, #159, #160, #181, #202 | 6 | #202 = API gateway (ADR-0009) — implementation merged via PR #204; issue still open (see §4) |
| Security & integrity | #144, #162, #180 | 3 | #144 RLS/tenant isolation; #180 production-shaped SAST/DAST/chaos/load |
| Product surfaces (customer & staff) | #123, #125, #128, #141, #142, #145, #146, #147, #148, #163 | 10 | #123 Command MVP live via PR #129, open for deferred scope; #125 Inspect console not yet built; #163 is the first candidate consumer of the #196 asset pipeline |
| Provider network & dispatch | #139, #140, #183 | 3 | Canonicals after duplicate reconciliation (§4) |
| Jobs & garage backend | #165, #169, #170 | 3 | #170 capacity API merged via PR #203; issue still open (see §4) |
| Service lifecycle | #156, #157, #158 | 3 | Estimates/approvals; warranties; roadside/towing escalation |
| Inspection platform backend | #179, #184, #185, #186 | 4 | Wave 3 follow-ups; #186 is security-critical (short-TTL signed evidence URLs) |
| Parts & marketplace | #149, #150, #151, #164 | 4 | Includes transaction policy/disputes (#151) |
| Payments & monetization | #177, #178 | 2 | #178 includes production payment-provider go-live checklist and partial refunds |
| Vehicle data & intelligence | #152, #153, #161, #176, #182 | 5 | Includes Vehicle Passport provenance/odometer (#161) |
| Graduate-mechanic track follow-ons | #166, #167, #168 | 3 | Phases 2–4 of #126 |
| B2B & future/conditional | #171, #172, #173, #174, #175 | 5 | #173 is discovery-only pending licensed partner/legal review (per #154 guardrails) |
| Asset pipeline | #196 | 1 | Pexafy licensed-asset pipeline convention — docs delivered by the PR that first ships `docs/ASSETS.md` |
| **Total** | | **54** | |

## 4. Governance notes (factual, current as of 2026-11-01)

- **`dev`-branch auto-close semantics.** GitHub auto-closes issues only for PRs merged into the default branch (`main`). Since PR #197 established the two-branch model, feature PRs merge into `dev` and their `Closes #N` trailers do **not** close issues. Live examples: #170 (capacity API delivered by PR #203) and #202 (gateway delivered by PR #204) remain open. Reconcile delivered-but-open issues by manual close with evidence links, and do not read open status as "nothing merged".
- **Duplicate reconciliation.** Issues #130–#137 are closed duplicates of the canonical open issues #139–#146 (verified: titles match pairwise, duplicates closed). Per #154, all new work targets #139–#146; the duplicate issues retain their original acceptance criteria in their descriptions.
- **Production blockers.** #10 (Actions billing lock) remains the standing CI constraint — no workflow files are added; local/manual gates are used instead (per #154 governance). #121 and #122 are closed: the deploy chain is #120 → #127 → #198 → #199 (degraded boot) and production routing for staff consoles landed via #200/#201. The release-readiness picture must still carry the open hardening gaps #180 (SAST/DAST/chaos/load) and #181 (GitOps/SLOs).
- **Shipped ≠ closed.** Per #154, shipped status requires implementation/tests/docs evidence. #123 (Command) has its MVP merged (PR #129) but stays open for its deferred scope (#128); #125 (Inspect console) has no merged implementation. Verify claims against code and contracts, not issue state alone.
- **Coverage reconciliation.** §2 accounts for all 79 merged PRs (groups sum to 79); §3 accounts for all 54 open issues (areas sum to 54). If GitHub shows different counts later, re-run the two list commands and update this file via an issue-linked PR.
