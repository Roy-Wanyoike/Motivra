# ADR-0006: Licensing (Apache-2.0) and Contribution Intake (DCO 1.1)

- **Status:** Accepted — owner-authorized decision executed by autonomous org, 2026-09-08
- **Date:** 2026-09-08
- **Deciders:** Owner (Roy Wanyoike) authorized the direction; executed by license-01 per issue #7
- **Related:** ADR-0001 (monorepo layout — license applies repo-wide), `CONTRIBUTING.md` (DCO section), `LICENSE` (Apache-2.0 text), issue #10 (Actions billing — future DCO check), issue #7

## Context

The repository is public from day one, and issue #7 requires the licensing and contribution-intake model to be fixed before the first external contribution merges. Several facts frame the decision:

1. **The moat is data, not code.** Motivra's defensibility sits in the Vehicle Passport dataset, verified-history network effects, technician/garage coverage and the Intelligence layer trained on that corpus. An Apache-2.0 grant of the codebase does not transfer any of those assets — the operational data (and the customer/tenant relationships producing it) is not in the repo and never will be.
2. **Commercial platform ambitions are real.** The roadmap names a 7-stream revenue model, and investor expectations include defensibility. But investors in this class of business underwrite the data network effect and execution speed, not code secrecy in a repo that is already public.
3. **Africa-first market entry depends on trust and integrator reach.** Fleet operators, insurers, garage partners and eventually M-Pesa-scale integrators must be able to build on the platform without legal review friction; a permissive, patent-clear license lowers that barrier.
4. **Recruiter and community optics matter.** A visible OSS posture with clean, lightweight governance (a LICENSE file, an ADR trail, a sign-off flow instead of legal paperwork) attracts senior engineers who self-select out of heavyweight CLA processes.
5. **The team is small and mostly autonomous agents.** Any intake process requiring manual legal review per PR would throttle the build. The intake mechanism must be automatable and enforceable at commit time.

## Decision

**License the entire repository under the Apache License 2.0** (`LICENSE`, appendix copyright: "Copyright 2026 Roy Wanyoike"), effective now, covering all code, contracts and documentation in this repo.

**Contribution intake is DCO 1.1, not a CLA.** Every commit must carry a `Signed-off-by` line (`git commit -s`) certifying the contributor has the right to submit the work under Apache-2.0 (see `CONTRIBUTING.md` → "License and Developer Certificate of Origin (DCO)"). The DCO is a lightweight origin attestation that preserves the project's ability to relicense per-component later if a premium module needs a different regime — without ever demanding blanket copyright assignment from contributors.

**Per-component licensing is revisited before Wave 5 ("Money").** If premium/commercial modules (e.g. advanced Intelligence features, enterprise fleet tooling) require a source-available or proprietary regime, they will be extracted into separately licensed components with an explicit ADR at that time — the monorepo layout (ADR-0001) keeps those contexts lift-out-ready. Until then, one license applies to everything, because mixed-license licensing discipline across 13+ contexts is a tax this team size cannot pay yet.

## Alternatives considered

**MIT.** Maximum simplicity, closest thing to a default choice. Rejected: MIT carries **no explicit patent grant**, which matters for a platform adjacent to dispatch logistics, attestation and AI-assisted diagnostics — patent exposure without a grant is a silent liability for adopters and for contributors alike. Apache-2.0 costs nothing extra in practice and buys the patent clause plus a defined contribution-intake posture.

**AGPL-3.0.** Strongest copyleft; would protect against closed-source forks of the code. Rejected: it **deters exactly the commercial integrators** the Africa-first strategy needs (fleet SaaS, insurer systems, garage ERP vendors — none will embed AGPL code in hosted products without counsel review), it contradicts the recruiter-visible OSS-posture goal, and the moat argument makes code protection the wrong axis anyway. Copyleft defends code that is not the crown jewel while taxing the ecosystem that is.

**BSL 1.1 (Business Source License).** Source-available with a change-date fallback. Rejected **today**: extra license complexity, unclear guidance for ordinary contributors, and it signals "not really open" — the opposite of the community and recruiter intent. Retained as the **natural candidate for premium modules** if the Wave 5 revisit triggers: per-component BSL keeps commercial protection where it pays for itself without touching the community core.

**Proprietary (all-rights-reserved notice).** Keeps every option open on paper. Rejected: a public repo with no license grants nobody any rights, which **kills the community signal** — no external contributor, employer or integrator can legally build on it. It converts "public" from an asset into a liability and locks out the contributor growth the roadmap depends on.

## Consequences

**Positive**

- **Patent grant** travels with the code: contributors and adopters get an explicit, defensive patent license, reducing the largest hidden risk of MIT-style adoption.
- **Employer- and integrator-friendly:** Apache-2.0 is on every corporate allowlist, so partner and enterprise usage requires no legal exceptions.
- **Contributor trust:** DCO sign-off is a one-line habit (`git commit -s`), not a legal agreement to review — the lowest-friction intake that still produces a per-commit audit trail.
- **Recruiter-visible governance:** LICENSE + ADR + DCO form a legible, professional OSS posture on the repo's front door.
- **Relicensing optionality preserved:** no CLA means no copyright assignment, but DCO attestation keeps per-component relicensing of contributed code legally clean if the Wave 5 revisit fires.

**Negative / costs**

- **Competitors may fork the code.** Accepted and mitigated: the moat is the data network effects (passport history, technician coverage, Intelligence corpus), not the code; a fork inherits neither the data nor the operating machine.
- **Apache-2.0 requires NOTICE/appendix hygiene** — derivative works must carry the license text; a `NOTICE` file will be added the moment a distribution surface actually ships.
- **DCO enforcement is not yet automated.** Sign-off is enforced by review today; an automated DCO check in CI is **deferred until the GitHub Actions billing lock (issue #10) is resolved** and CI is restored as the merge gate — noted as follow-up work on that tracker.
- **Premium-module licensing is deferred, not decided:** if Wave 5 arrives without the revisit ADR, the default remains repo-wide Apache-2.0; the revisit trigger is a scheduled decision point, not an afterthought.
