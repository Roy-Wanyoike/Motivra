# Backend — Identity & Security

Authentication, RBAC roles, refresh sessions and the append-only audit trail
for Motivra. The identity/security engineer owns this zone; see
`docs/ARCHITECTURE.md` §2 and ADR-0004 (security baseline).

Owned by the identity/security owner; do not edit outside your zone.

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

## Audit actions

Written to `audit_log` for every register, login, refresh (rotation) and
logout (both refresh-token and session-id paths): `identity.user.registered`,
`identity.user.login`, `identity.session.refreshed`,
`identity.session.revoked`. Entries carry actor, object, tenant (when
applicable) and metadata (device, IP, email on registration). **Audit
failures fail the request** — signals are never dropped silently.

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
(users, user_roles, sessions, audit_log). Backward-compatible changes only
(expand, migrate, contract); down files are the rollback path.

## Do NOT

- Do not issue or sign tokens anywhere else; identity is the sole issuer.
- Do not log or serialize `PasswordHash` or plaintext passwords; `Profile`
  and `ListUsers` strip hashes before returning.
- Do not swallow audit or store errors — convert and return them.
- Do not add client-side authorization decisions; RBAC is server-side data.
- Do not weaken the password policy or argon2id parameters for tests; tests
  use the same constants.
