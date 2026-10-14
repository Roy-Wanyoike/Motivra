# PII Handling — inventory, retention and obligations

Status: production-readiness pass (issue #85). This document is the single
source of truth for what personal data Motivra stores, where it lives, how
long it is kept and how it is removed. It is written against the actual
migration schemas (`backend/migrations/**`) — update it with every schema
change.

## Why this document exists

Motivra operates in Kenya: the **Data Protection Act, 2019 (Kenya DPA)**
applies to the personal data in this system (registration data, device
identifiers, IP addresses, vehicle registrations tied to identifiable
owners). The obligations this document addresses: lawful minimisation,
storage limitation (retention), security safeguards, and the ability to
erase. It does not replace legal advice — it is the engineering contract
that makes compliance auditable.

## PII inventory (table by table, matching migrations)

| Table / location | Fields | Sensitivity | Notes |
| --- | --- | --- | --- |
| `users` (identity 000001) | `email` (unique), `phone`, `full_name` | Direct PII | `password_hash` is argon2id (m=64MiB,t=3,p=4) — credential material, not retrievable PII |
| `sessions` (identity 000003) | `ip_address`, `device_name`, `refresh_token_hash` | PII (IP), quasi-PII (device) | `device_name` is client free-text — **redacted through `platform.RedactFreeText`** before persistence (issue #85); refresh tokens stored only as HMAC-SHA256 digests |
| `audit_log` (identity 000004) | `metadata` jsonb — historically email, ip_address, device_name | PII (IP, email) | `device_name` now redacted; registration metadata email usage is being migrated to `platform.RedactEmail`; table is append-only (000006 trigger); retention via `scripts/purge_expired.sql` |
| `vehicles` (vehicles 000001) | `vin` (unique, normalized), `plate`, `owner_user_id` | Quasi-PII / personal-data-adjacent | VIN + plate identify a real vehicle and, via registries, its owner |
| `vehicle_service_history` (vehicles 000003) | `recorded_by` (uuid), free-text `summary`, `evidence_ref` | Low | Append-only (trigger); summaries must not contain personal notes — UI copy enforces event summaries |
| `service_requests` / `jobs` / `job_assignments` / `job_transitions` (jobs 000001–000004) | `customer_id`, `technician_id` (uuid references to users) | Indirect PII (references only) | No free-text addresses beyond `address_text` supplied by the requester |
| Request logs (stdout → Loki) | request metadata only: method/path template/status/duration/remote/request_id/trace_id/tenant_id | IP address (remote) | Path is the route **template**, never raw query strings; no user agent, no body, no tokens (grep-audited) |

Not stored anywhere: payment card data, national IDs, precise geolocation
history (dispatch scoring uses coarse service areas), biometrics.

## Where PII must never appear

1. **Logs and traces** (ADR-0005): passwords, tokens, raw emails and phone
   numbers are forbidden. `platform.RedactFreeText` sanitises every
   client-supplied free-text value before it enters audit metadata;
   the request logger records route templates, not raw URLs.
2. **Client-side storage**: access tokens live in memory/localStorage by
   the current customer-app design (hardening follow-up #90 tracks the
   httpOnly-cookie migration); no PII beyond tokens and display data.
3. **Backups**: covered by the same retention rules — a restored backup is
   bound by this document.

## Retention schedule (executable, not aspirational)

| Data | Retention | Mechanism |
| --- | --- | --- |
| Expired/revoked `sessions` | purged 30 days after expiry/revocation | `make purge-expired` (scripts/purge_expired.sql), scheduled daily |
| `audit_log` entries | 18 months (configurable `AUDIT_MONTHS`) | `make purge-expired PURGE_AUDIT=true`, scheduled monthly; the ONLY sanctioned delete path (guarded trigger window); every invocation logged in the operations log |
| Backups | operator policy (recommend 90 days rolling) | retention on the backup storage location |
| Loki request logs | 15 days (Prometheus TSDB) / 168h reject-old (Loki) | compose observability stack config |

## Erasure (right to erasure, DPA s.40-42 equivalent)

The account model soft-deletes (`users.status='deleted'`). Full erasure
flows are tracked in the audit-coverage issue (#88, erasure flow). Until
that ships, manual erasure procedure:

1. Soft-delete the account (admin action).
2. `psql "$MOTIVRA_DATABASE_URL"` — delete the user's sessions directly
   (sessions are not append-only).
3. Audit rows referencing the actor are **retained** (legitimate interest:
   security evidence) with the actor id only — the id no longer resolves
   to personal data once the users row is expunged in step 4's data purge
   script (tracked in #88).
4. Vehicles and history are business records; they persist unless the
   vehicle is also removed by the owner request.

## Security safeguards in place

- Transport: TLS terminates at the edge (HSTS preload, #82); internal
  service traffic runs on an isolated compose network with loopback-only
  host bindings.
- At rest: managed-Postgres encryption is the deployment operator's
  control (documented in RUNBOOK production deployment); no field-level
  encryption yet — tracked as a follow-up if regulated data categories
  are added.
- Access: every privileged API surface is RBAC-gated (audited per route);
  database access is one service credential per stack — per-service DB
  roles are tracked in #88.
- Audit: authentication events are fail-closed audited (a failed audit
  write fails the request); the audit trail is physically append-only.

## Change discipline

Any PR that adds or changes a column holding personal data must update
this inventory and the retention schedule in the same change. The
integrity check (`make integrity-check`) verifies the append-only
triggers stay armed; the purge script is the only bulk delete path.
