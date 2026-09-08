# Security Policy

Motivra handles authentication credentials, vehicle history, and (over time)
payments data for a vehicle-intelligence platform. This document describes how
to report vulnerabilities, what is supported, and the security rules the
project holds itself to.

## Supported versions

Motivra is **pre-1.0** and ships no releases or tags yet. Security fixes apply
to:

| Version / ref | Supported |
| --- | --- |
| `main` branch (HEAD) | ✅ Yes |
| Any older commit, tag, or fork | ❌ No — upgrade to `main` |

There are no backport branches. If you run Motivra, run the current `main`.

## Reporting a vulnerability

**Please do not open a public issue for security problems.**

Report via **GitHub Private Vulnerability Reporting** on this repository:

1. Go to the **Security** tab of
   [Roy-Wanyoike/Motivra](https://github.com/Roy-Wanyoike/Motivra).
2. Choose **Report a vulnerability** (private vulnerability reporting).
3. Include: affected paths/services, reproduction steps or PoC, impact
   assessment, and any logs/evidence (redact real user PII).

There is **no security email yet** — GitHub private reporting is the only
disclosure channel. If private vulnerability reporting is unavailable for some
reason, contact the owner `@Roy-Wanyoike` directly via GitHub.

Please give us a reasonable window to fix before any public disclosure.

## Response targets

| Stage | Target |
| --- | --- |
| Acknowledgement of report | **72 hours** |
| Triage (severity + impact assessment) | **7 days** |
| Fix or mitigation — Critical | **30 days** |
| Fix or mitigation — High | **60 days** |
| Fix or mitigation — Medium/Low | Best-effort, or explicitly documented as accepted risk with rationale |

During the current single-maintainer phase these are targets, not SLAs; if a
deadline will slip, the reporter will be updated with status. Accepted-risk
decisions are documented in the issue thread.

Severity is assessed on impact: tenant isolation breakage, authentication
bypass, vehicle-history tampering (it must remain append-only), and anything
touching money (integer minor-unit ledger) rank highest.

## Secrets policy

- **Never commit secrets.** No API keys, tokens, passwords, private keys, or
  connection strings in code, config, tests, docs, or commit history.
  `.env` / `.env.*` are gitignored (only `.env.example` is committed).
- **Configuration comes from the environment.** Services read settings such
  as `MOTIVRA_DATABASE_URL`, `MOTIVRA_JWT_SECRET`, and `MOTIVRA_NATS_URL`
  from process environment variables (`backend/platform/config.go`); provide
  them via local `.env` files (untracked) or a proper secret manager when
  deployed. Never bake them into images or repos.
- **If a secret is leaked (committed, posted, or exposed): rotate it
  immediately** — revoke/replace first, then clean history if feasible, then
  document the incident in a (non-sensitive) issue. Assume any leaked token
  is compromised from the moment of exposure; for the GitHub PAT used during
  the founding phase, rotation was already flagged as an owner action.
- **Local storage guidance:** keep secrets out of sync/backups where
  possible; use OS keychains or an encrypted store rather than plaintext
  files when practical; scope tokens to the minimum needed and expiry-bound
  them.

## Security-relevant defaults (see docs/ADR-0004)

The baseline is decided and binding in
[docs/adr/0004-security-baseline.md](docs/adr/0004-security-baseline.md):

- **Argon2id** password hashing (memory-hard, per-user salt, parameterized
  hashes) — never unsalted or fast hashes.
- **HS256 JWT access tokens**, short-lived (~15 min TTL), issued behind an
  issuer-agnostic `TokenIssuer`/`TokenVerifier` abstraction; **opaque refresh
  tokens** (non-JWT) stored hashed, **rotated on every use**, with
  reuse-of-rotated-token treated as a theft signal that revokes the session.
- **Server-side RBAC** with a fixed role matrix
  (`CUSTOMER` … `SUPER_ADMIN`); no client-side authorization decision is
  load-bearing.
- **Append-only vehicle history** enforced at the database layer — no update
  or delete paths on history records.
- **Money is integer minor-units** end to end.

## AI advisory-only boundary

AI components in Motivra are **advisory only**: they may propose causes,
inspections, and estimates; they **never authorize work, never mutate vehicle
history, and are never a financial source of truth**. AI outputs carry
`prediction`, `confidence`, `source`, and verification state, and enter
durable records only via a normal, attributed human write path. AI services
hold no database grants on ledger or history tables.

Details: [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) (Intelligence/AI
boundary section) and
[docs/adr/0004-security-baseline.md](docs/adr/0004-security-baseline.md).

## Security scope notes

- GitHub Actions is currently disabled account-wide (billing lock, tracked in
  issue #10); **local quality gates are the interim merge gate**, and `main`
  is protected by a ruleset requiring PRs and forbidding force-push/deletion.
- Dependabot alerts and secret scanning: enablement is handled by the
  coordinator via API/UI; status is recorded in the security PR and QA
  report.
