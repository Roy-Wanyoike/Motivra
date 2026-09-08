# Motivra Spec — Founding Directive & Provenance

This directory preserves the founding directives of the Motivra project — the
instructions under which the autonomous build organization was constituted — so
that they survive independently of any chat session, local workspace, or
individual machine.

> Audit requirement this directory answers: **"ensure nothing is left or lost
> in the git history locally."** Every founding artifact now has a durable,
> stated home — in this repository, on GitHub, or explicitly and deliberately
> in the local internal archive. The full mapping is in the
> ["Nothing lost" audit trail](#nothing-lost-audit-trail) below.

---

## 1. The v1 directive (chat history — not recoverable verbatim)

The first build directive was given in conversation and exists **only as chat
history**. Its verbatim text cannot be recovered; what follows is a faithful
summary of its essentials, from which the founding work (README, docs,
ADRs, Wave 0–2 execution) was actually carried out.

**Shape:** a coordinated agent organization of **26 agents across 8 waves** —
not a single coding agent, but a virtual technology company with a coordinator
dispatching bounded, parallel work.

Essentials preserved from v1:

- **Coordinated agent org with bounded ownership zones.** Every agent owned
  an explicit zone (files/directories); agents never wrote outside their
  zone. Cross-zone changes required a dependency request to the owning agent.
- **Issue → branch → PR → CI → review flow.** All work tracked as GitHub
  issues; all changes landed via PRs against `main`; **no direct pushes to
  main**.
- **Integration windows between waves.** Each wave of parallel work was
  followed by an integration window in which contracts were reconciled,
  statuses were recorded in the agent registry, and the next wave was seeded.
- **Non-negotiables** (carried forward unchanged into v2):
  - **Money is integer minor-units** (e.g. cents/kobo) — never floats.
  - **Vehicle history is append-only** — records are never updated or
    deleted; corrections are new entries.
  - **AI is advisory-only** — it proposes, never authorizes work, never
    mutates vehicle history, and is never a financial source of truth.
  - **No direct pushes to main** — main is protected; everything lands by PR.

## 2. The v2 directive (full text — committed beside this README)

The second, expanded directive was supplied as a document upload and is
preserved here **verbatim**:

- **File:** [`MOTIVRA-MASTER-DIRECTIVE-v2.md`](./MOTIVRA-MASTER-DIRECTIVE-v2.md)
- **Length:** 3,182 lines, imported byte-for-byte (line count verified at
  import time against the source upload).
- **Content:** a **30-agent organization** (A01–A30) working in **10 waves**
  (Wave 0 discovery through Wave 10 production), covering **9 product lines**
  (Motivra Drive, Tech, Inspect, Garage, Fleet, Dealer, Command, Intelligence,
  Developer), with:
  - **§74 — Investor-grade features:** the standard the product must meet to
    be credible to investors.
  - **§75 — Recruiter-grade engineering:** the standard the codebase must
    meet to be credible to engineering recruiters (quality gates, tests,
    docs, commit/PR discipline).

v2 supersedes v1 where the two differ (e.g. org size 26 → 30, waves 8 → 10,
explicit product-line enumeration). v1's non-negotiables listed above are
restated in v2 and remain binding.

## 3. ChatGPT share link — login-gated, pending owner verification

The founding conversation is also referenced by a ChatGPT share link:

- <https://chatgpt.com/share/6a9fd314-06b4-83ea-8419-dbf80bacb057>

**Status: unread.** The link is login-gated; automated retrieval returned only
the login shell, not the conversation content. It is therefore **pending
verification by the repo owner**, who should open it in a browser and confirm
whether it contains any directive material not already captured in §1 and §2
above. If it does, it should be appended here or to the v2 file as a third
provenance layer.

## 4. Precedence rule: ADRs override the spec

Where the spec and the merged Architecture Decision Records **diverge, the
ADRs win**:

> The spec is the **vision and directive** — it describes intent, standards,
> and the shape of the work. The ADRs are the **binding architecture** —
> they record what was actually decided and accepted, with context and
> consequences.

In practice: implement per the ADRs; treat the spec's corresponding passages
as superseded on that point; if the divergence is a genuine mistake in the
ADR, fix it by a **new ADR**, not by reinterpreting the spec silently.

Merged ADRs (each overrides the spec where they diverge):

| ADR | Decision |
| --- | --- |
| [ADR-0001](../adr/0001-bounded-contexts-and-monorepo-layout.md) | Bounded contexts & monorepo layout |
| [ADR-0002](../adr/0002-event-schema-and-jetstream-topics.md) | Event schema & JetStream topics |
| [ADR-0003](../adr/0003-data-ownership-and-migrations.md) | Data ownership & migrations |
| [ADR-0004](../adr/0004-security-baseline.md) | Security baseline |
| [ADR-0005](../adr/0005-observability-and-slos.md) | Observability & SLOs |

See also [docs/ARCHITECTURE.md](../ARCHITECTURE.md) for the binding system
architecture and the agent registry at
[docs/engineering/agents.md](../engineering/agents.md).

---

## "Nothing lost" audit trail

Mapping of **every founding artifact** to its durable location:

| # | Founding artifact | Origin | Durable location | Status |
| --- | --- | --- | --- | --- |
| 1 | **v2 master build directive** (3,182 lines; 30 agents, 10 waves, 9 product lines, §74/§75) | Local chat upload (single machine) | [`docs/spec/MOTIVRA-MASTER-DIRECTIVE-v2.md`](./MOTIVRA-MASTER-DIRECTIVE-v2.md) — committed verbatim | ✅ Preserved in-repo |
| 2 | **v1 directive** (26-agent / 8-wave governance) | Conversation history (chat only) | Essentials summarized in §1 of this README | ✅ Preserved (essentials; verbatim text not recoverable) |
| 3 | **ChatGPT share link** `6a9fd314-06b4-83ea-8419-dbf80bacb057` | Conversation history | URL in §3 of this README | ⏳ Login-gated, unread — pending owner verification |
| 4 | **Issue bodies** (drafted as `scripts/issue_*.md`) | Local workspace | Mirrored into **GitHub issues #1–#7 and #24–#31** | ✅ On GitHub |
| 5 | **PR bodies** (drafted locally, e.g. `scripts/pr_body.md`, `scripts/pr_platform.md`, `agent-reports/pr-body-arch01.md`, `agent-reports/vehicles-pr-body.md`) | Local workspace | Mirrored into **GitHub PRs #1, #8, #9, #14–#17, #20, #21, #23** | ✅ On GitHub |
| 6 | **Local helper scripts** (`scripts/issue_*.md`, `scripts/pr_*.md`, `scripts/ruleset.json`, `scripts/chatgpt_share.*`) | Local workspace | Mirrored into the GitHub issues/PRs listed above; originals remain in the local internal archive | ✅ Mirrored / 🗄 local archive |
| 7 | **Agent run reports** (`agent-reports/*.md` — per-agent work narratives) | Agent workspaces | **Local internal archive — deliberately not committed.** They record internal agent coordination (dispatches, handoffs, incidents), not project truth. Project truth lives in code, ADRs, docs, issues, and PRs. | 🗄 Local archive (by design) |
| 8 | **Worklog** (multi-agent work log, `/home/z/my-project/worklog.md`) | Coordinator | **Local internal archive — deliberately not committed.** Same rationale as #7. | 🗄 Local archive (by design) |

Legend: ✅ durable · ⏳ awaiting human step · 🗄 local internal archive
(deliberate).

**Residual risk:** item 2 (v1 verbatim) and item 3 (share link) are the only
artifacts without full verbatim preservation. Item 3 has a defined owner
action; item 1 of this table (v2, the operative directive) is fully preserved.
