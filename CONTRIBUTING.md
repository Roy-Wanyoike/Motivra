# Contributing to Motivra

Thanks for your interest in the build. The process is deliberately opinionated — it is how a distributed team ships production-grade work without stepping on each other.

## The flow

The repository runs a **two-branch model**, with both branches protected by
GitHub branch-protection rulesets: `dev-branch-protection` (ruleset ID
26035216, applies to `refs/heads/dev`) and `main-branch-protection` (ruleset
ID 22533278, applies to the default branch, `main`). Both rulesets require a
pull request, block branch deletion and non-fast-forward pushes, require
extra approval for unattributed changes, and have no bypass actors — never
push directly to `dev` or `main`.

1. **Claim or open an issue.** Every meaningful change starts as a GitHub issue using the templates (feature / architecture / spike). Check the [ideas board](docs/IDEAS.md) for context.
2. **Branch from `dev`.** Exactly two branches are permanent: `dev` (integration) and `main` (production). Everything else is temporary and originates from `dev`. Naming: `feature/<issue>-<slug>` or `fix/<issue>-<slug>` (e.g. `feature/203-offline-outbox`). One owner per branch.
3. **Work inside your ownership zone.** Bounded contexts own their tables, events and workflows. Cross-zone changes need a dependency request on the owning issue.
4. **Open a PR targeting `dev`.** Every PR — feature, fix, dependency bump, docs — targets `dev`, never `main`. Fill the template: problem, solution, architecture/DB/API/event impact, security, testing, observability, rollback. No vague PRs.
5. **Pass CI gates.** Build, lint, tests, security scan, migration validation, coverage.
6. **Review and merge.** The PR references its issue; merging auto-closes it. Temporary branches may be deleted after merge; `dev` and `main` are never deleted.
7. **Release.** A release is an explicitly authorized release PR from `dev` to `main`, opened only after integration validation on `dev` has passed. Vercel deploys production from `main` only — see the [runbook](docs/RUNBOOK.md) §6.

### PR checklist

- [ ] PR targets `dev` (an authorized release PR to `main` is the only exception)
- [ ] References its issue; no unrelated files modified
- [ ] CI gates pass
- [ ] Documentation reflects the actual implementation — verified, never aspirational text
- [ ] Links, branch names and IDs cited in docs are correct against the live repository
- [ ] Every commit carries a DCO sign-off (`git commit -s`)

## Non-negotiables

- No incomplete features: no TODOs, no fake APIs, no mocked production flows.
- History is append-only where the domain says so (vehicle history, evidence).
- Money in integer minor units; never floats.
- Never trust client-provided prices, ownership, permissions or state.
- AI is advisory: prediction + confidence + source + verification state.
- External dependencies get designed failure paths.
- One shared vocabulary: [GLOSSARY.md](docs/GLOSSARY.md).

## Local development

`make dev` boots the dev dependencies (Postgres, Redis, NATS, MinIO) via Docker Compose; `make stack-up` runs the full application stack. The service template in `/backend/platform` is the reference for wiring any new service — see the [runbook](docs/RUNBOOK.md) for the complete walkthrough.

## Questions

Open a discussion or pick up a [`good first issue`](https://github.com/Roy-Wanyoike/Motivra/issues?q=is%3Aissue+is%3Aopen+label%3A%22good+first+issue%22).

## License and Developer Certificate of Origin (DCO)

Motivra is licensed under the [Apache License 2.0](LICENSE). By contributing, you agree your contributions are licensed under the same terms. Every commit must carry a [DCO 1.1](https://developercertificate.org/) sign-off — simply commit with `git commit -s` (e.g. `git commit -s -m "feat(vehicles): add VIN resolution"`), which appends a `Signed-off-by: Your Name <you@example.com>` line certifying you have the right to submit the work under Apache-2.0. No CLA is required.
