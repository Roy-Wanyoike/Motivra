# Contributing to Motivra

Thanks for your interest in the build. The process is deliberately opinionated — it is how a distributed team (human and agent) ships production-grade work without stepping on each other.

## The flow

1. **Claim or open an issue.** Every meaningful change starts as a GitHub issue using the templates (feature / architecture / spike). Check the [ideas board](docs/IDEAS.md) for context.
2. **Branch from `main`.** Naming: `agent/<domain>/<topic>` (e.g. `agent/dispatch/scoring`). One owner per branch.
3. **Work inside your ownership zone.** Bounded contexts own their tables, events and workflows. Cross-zone changes need a dependency request on the owning issue.
4. **Open a PR with the template filled.** Problem, solution, architecture/DB/API/event impact, security, testing, observability, rollback. No vague PRs.
5. **Pass CI gates.** Build, lint, tests, security scan, migration validation, coverage.
6. **Review and merge.** The PR references its issue; merging auto-closes it.

## Non-negotiables

- No incomplete features: no TODOs, no fake APIs, no mocked production flows.
- History is append-only where the domain says so (vehicle history, evidence).
- Money in integer minor units; never floats.
- Never trust client-provided prices, ownership, permissions or state.
- AI is advisory: prediction + confidence + source + verification state.
- External dependencies get designed failure paths.
- One shared vocabulary: [GLOSSARY.md](docs/GLOSSARY.md).

## Local development

Coming with Wave 1 (platform foundation): `make dev` boots Postgres, Redis, NATS, Temporal, MinIO and the core services via Docker Compose. Until then, the platform-foundation service template in `/backend/platform` is the reference for wiring any new service.

## Questions

Open a discussion or pick up a [`good first issue`](https://github.com/Roy-Wanyoike/Motivra/issues?q=is%3Aissue+is%3Aopen+label%3A%22good+first+issue%22).
