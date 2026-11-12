# Backend — Identity & Security

Authentication, RBAC roles, refresh sessions and the append-only audit trail
for Motivra. See `docs/ARCHITECTURE.md` §2 and ADR-0004 (security baseline).

## Responsibilities

- **Account ownership**: the `users` table and password lifecycle
  (argon2id, PHC-encoded, 64 MB / t=3 / p=4, policy 10-128 chars).
- **Token issuance** (the only issuer in Motivra): short-lived HS256 access
  tokens (15 minutes) with `sub`, `tenant_id`, `role`, `roles`, `sid`,
  `iss`, `aud`, `iat`, `exp`; opaque 256-bit refresh tokens (30 days,
  rotated on every use) persisted only as keyed HMAC-SHA256 digests.
- **Session management**: revocation, rotation, reuse/expiry rejection.
- **The fixed nine-role RBAC vocabulary** consumed by platform middleware
  and every context's `authz` matrix.
- **The audit trail**: append-only `audit_log`; security-relevant failures
  (including audit write failures) fail the request.
- **Technician skill-verification** (issue #166): the server-authoritative
  `unverified | pending | verified | rejected` lifecycle attribute on the
  technician profile, its append-only decision records and the staff-only,
  audit-logged verification API.

Everyone else **validates** tokens through `backend/platform`
(`NewJWTValidator` + `AuthMiddleware`) — issuance never leaks out of here.

## HTTP API (handlers in `http.go`, contract in `contracts/identity/openapi.yaml`)

| Method | Path | Auth | Purpose |
|---|---|---|---|
| POST | `/v1/auth/register` | public | Create account (CUSTOMER role), 201 + `AccessResponse` |
| POST | `/v1/auth/login` | public | Authenticate, 200 + `AccessResponse` (identical 401 for unknown email / bad password) |
| POST | `/v1/auth/refresh` | public | Rotate refresh session, 200 + new `AccessResponse` |
| POST | `/v1/auth/logout` | public (refresh token = credential) | Revoke session, 204; unknown/revoked tokens get 401, not 404 |
| GET | `/v1/users/me` | bearer | Authenticated profile (hash never serialized) |
| GET | `/v1/admin/users` | bearer + ADMIN/SUPER_ADMIN | Paginated account listing (`?limit` 1-100 default 20, `?offset`) |
| GET | `/v1/technician/verification-status` | bearer | The caller's verification status + newest decision record (issue #166) |
| POST | `/v1/admin/technicians/{technicianID}/verification/approve` | bearer + staff matrix | Record a VERIFIED outcome: decision record + status stamp + audit entry in one transaction (issue #166) |
| POST | `/v1/admin/technicians/{technicianID}/verification/reject` | bearer + staff matrix | Record a REJECTED outcome (reason required), same transactional seam (issue #166) |

Errors are always `platform.ErrX` values rendered as
`application/problem+json` by `platform.WriteError`. Client IPs for audit
come from `X-Forwarded-For` (first hop) or `RemoteAddr`.

## RBAC

Server-side only, per ADR-0004: `CUSTOMER, TECHNICIAN, DISPATCHER, GARAGE,
FLEET_ADMIN, SUPPORT, FINANCE, ADMIN, SUPER_ADMIN`. The access token carries
`role` (highest-priority, precedence SUPER_ADMIN > ADMIN > FINANCE >
SUPPORT > FLEET_ADMIN > GARAGE > DISPATCHER > TECHNICIAN > CUSTOMER) and
`roles` (all grants); platform `RequireRole` gates coarse permissions per
route. Middleware is never the only check for sensitive resources.

**Verification decisions (issue #166)**: ADMIN and SUPER_ADMIN only — the
decision of record reuses the fixed role set (no dedicated VERIFIER role;
see `authz.go` for the enumerated matrix and its test). The gate lives in
the SERVICE, not the middleware, so a refused attempt is itself audited
(`identity.technician.verification.denied`) — the issue requires the 403 to
leave an audit trail. Self-decisions are denied (separation of duties), and
the target must hold the TECHNICIAN role (anything else reads 404).

## Audit actions

Written to `audit_log` for every register, login, refresh (rotation),
logout (both refresh-token and session-id paths) and role grant:
`identity.user.registered`, `identity.user.login`,
`identity.session.refreshed`, `identity.session.revoked`,
`identity.role.granted`. Entries carry actor, object, tenant (when
applicable) and metadata (device, IP, email on registration). **Audit
failures fail the request** — signals are never dropped silently.

Technician verification (issue #166) adds
`identity.technician.verification.approved` / `.rejected` (object
`technician_verification`, metadata: technician, status, redacted reason,
evidence references, previous status) and `.denied` for refused attempts.
Each decision commits its record, the `users.verification_status` stamp and
the audit row in ONE transaction (fail-closed seam, proven by integration
test); `technician_verifications` is append-only (DB trigger) and covered
by `make integrity-check`.

## Events (ADR-0002)

Published via `platform.Publisher` on subject `motivra.identity.<event_type>`
after every write of the underlying fact has succeeded (user row + session +
audit for registration; grant row + audit for grants). `cmd/identity`
constructs a `platform.NATSPublisher` when `MOTIVRA_NATS_URL` is set (issue
#28, deferral 2); while the variable is unset the service runs with a nil
publisher and skips publishing:

| Event | When | Payload highlights |
|---|---|---|
| `user.created.v1` | registration fully succeeds | user_id, status, roles — never credential material or transport metadata |
| `role.granted.v1` | a role grant is persisted and audited | user_id, role, tenant_id? (omitted for personal grants) |

`role.granted.v1` is emitted by `Service.AssignRole` (store write → audit →
event); the admin HTTP route that will call it needs a contract addition and
is tracked with the role-management work. The envelope carries the acting
principal as `actor_id` and the platform request id as `correlation_id`;
`causation_id` stays null (these are request-initiated events).

## Token model

- Access: HS256 JWT, 15 minutes, shared secret distributed by env
  (`MOTIVRA_JWT_SECRET`, >= 32 bytes in production), validated by every
  service via platform.
- Refresh: 256-bit random base64url, 30-day idle cap, stored as
  `hex(HMAC-SHA256(key=issuer secret, msg=token))` — a database leak alone
  cannot verify guessed tokens (ADR-0004). Rotation revokes the presented
  row before minting its successor; the successor inherits device/IP.
- The issuer abstraction (`Issuer` + optional `RoleGrantsProvider` store
  capability) is the seam for a future external OIDC provider; consumers
  never change.

## Booting (`cmd/identity/main.go`)

`platform.Load`/`Validate` → logger → tracing + metrics → `NewPostgres`
(required in every environment) → `platform.MigrateUp` with
`identitymigrations.FS` and table `schema_migrations_identity` →
`NewPostgresStore` + `NewIssuer` + `NewService` → `platform.NewServer` with
database readiness check and `platform.AuthMiddleware` → `identity.Routes`
→ HTTP server with timeouts → `platform.Graceful` (pool close, metrics,
tracing flush).

## Migrations

Paired `NNNNNN_name.{up,down}.sql` under `backend/migrations/identity/`
(users, user_roles, sessions, audit_log, verification_tokens,
technician_verifications). Backward-compatible changes only
(expand, migrate, contract); down files are the rollback path.

## Do NOT

- Do not issue or sign tokens anywhere else; identity is the sole issuer.
- Do not log or serialize `PasswordHash` or plaintext passwords; `Profile`
  and `ListUsers` strip hashes before returning.
- Do not swallow audit or store errors — convert and return them.
- Do not add client-side authorization decisions; RBAC is server-side data.
- Do not weaken the password policy or argon2id parameters for tests; tests
  use the same constants.
