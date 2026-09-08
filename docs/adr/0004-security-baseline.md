# ADR-0004: Security Baseline — AuthN, AuthZ, Tenant Isolation, Money Integrity

- **Status:** Accepted
- **Date:** 2026-09-08
- **Deciders:** Principal Architect (arch-01), per issue #2
- **Related:** ADR-0001 (contexts), ADR-0003 (data ownership), ADR-0005 (observability/audit signals), `docs/ARCHITECTURE.md` §5, §6

## Context

Motivra handles data whose compromise has physical-world consequences (technician dispatch to a customer's home location), financial consequences (M-Pesa payments, the ledger), and trust consequences (the Vehicle Passport is marketed as tamper-evident history — a security failure is a product failure). It operates in markets where SMS is an auth channel of necessity, where device sharing is common, and where the abuse surface includes collusion between technicians and customers on estimates.

The security model must therefore be fixed early enough that Wave 1 identity work implements it, every context encodes it, and later waves (fleet APIs, dealer portals, developer platform) inherit it rather than reinterpret it. Decisions needed now:

1. Token format and session model, given no OIDC provider exists yet but one will come.
2. The authorization model across the nine internal roles and multi-tenant data.
3. Password/credential storage.
4. Financial integrity guarantees and the boundary of AI's write authority.
5. Secrets handling and webhook trust.

## Decision

### Authentication

- The **identity service issues short-lived HS256 JWT access tokens** (default TTL 15 minutes, configurable down). Claims: `sub`, `tenant_id`, `roles`, `session_id`, `iat`, `exp`, `iss`, `aud`.
- The issuing logic sits behind an **issuer-agnostic abstraction**: an internal `TokenIssuer`/`TokenVerifier` interface in identity, with HS256 as the first implementation. Swapping to an external OIDC/OAuth2 provider (RS256/EdDSA, JWKS discovery, authorization-code + PKCE for clients) later changes only the identity service's implementation — **consumers and middleware never change**.
- **Refresh tokens are opaque** (256-bit random, non-JWT), returned to clients and stored **hashed** (SHA-256 with per-token salt via HMAC) in the identity `sessions` table alongside device metadata, rotation state and expiry. Rotation on every use; reuse of a rotated token revokes the session (theft signal). Refresh TTL: 30 days idle-capped.
- **Validation middleware lives in `backend/platform`** and is issuer-agnostic by contract: it verifies signature, issuer, audience, expiry and required claims (`tenant_id`, `roles`), and refuses tokens with none. Services consume it as a dependency of every route group; a route is public only by explicit, reviewed declaration.

### Authorization

- **Server-side RBAC** with the fixed role set: `CUSTOMER, TECHNICIAN, DISPATCHER, GARAGE, FLEET_ADMIN, SUPPORT, FINANCE, ADMIN, SUPER_ADMIN`. No client-side authorization decisions are load-bearing, ever.
- **Permissions are enumerated per context** in each context's `authz` package: a role-to-permission matrix (e.g. `payments:refund` → FINANCE, ADMIN, SUPER_ADMIN; `dispatch:force_assign` → DISPATCHER, SUPER_ADMIN; `estimates:approve` → the customer principal owning the job — resource-level, not merely role-level). The matrix is data, testable, and rendered into docs so product and security review the same artifact.
- Enforcement is **two-layer**: platform middleware gates coarse permissions per route; the service layer re-checks resource-level authorization (ownership, assignment, state). Middleware alone is never the only check for anything sensitive.
- `SUPER_ADMIN` actions are additionally audit-logged with immutable records (actor, action, target, timestamp — write-once table, ADR-0003 append-only rules apply).

### Tenant isolation

- **`tenant_id` scoping is mandatory** on every multi-tenant table; it is part of the primary/foreign key design where the domain allows, never an optional column.
- The **repository layer enforces scope**: queries take a tenant context parameter and filter by it; no repository method offers an unscoped variant outside explicitly documented system-internal paths (background jobs that operate per-tenant carry the tenant context through).
- **Integration tests prove cross-tenant denial**: every context's test suite includes a case where a valid authenticated principal of tenant A requests a resource of tenant B and receives not-found/forbidden — CI fails without it.

### Passwords and credentials

- **Argon2id** for password hashing: memory 64 MB, iterations 1–3 (tuned upward as hardware allows; parameterized, stored with the hash), per-user random salt. No unsalted, fast, or homegrown hashes anywhere.
- Password policy: length-first (minimum 10), breach-list checking, no composition theater. SMS OTPs (for phone verification and step-up where MFA lands later) are rate-limited, expiring, single-use, and constant-time compared.

### Money and AI boundaries

- **Money is integer minor units only** — never floats, never client-formatted decimals round-tripped through the server. Currency is explicit on every amount-bearing record.
- The **ledger is authoritative**: every financial operation carries an idempotency key, transaction ID, audit record and provider reference. Balance-affecting state changes are transactional with ledger writes.
- **AI is advisory-only and can never write financial or history records.** AI outputs (predictions, causes, estimates suggestions, risk scores) carry `prediction`, `confidence`, `source`, `verification state`; they enter durable records only when a human actor confirms them through a normal, attributed write path. Architecturally, AI services have no database role grants on ledger or history tables (ADR-0003 role partitioning enforces this mechanically).

### Secrets, webhooks, and client trust

- **Secrets are environment-injected** (secret manager → env at deploy time), never committed. CI includes secret scanning; a leaked secret is rotated, not deleted from history and wished away.
- **Webhook signature verification is mandatory** on every inbound webhook route: provider-specific HMAC/signature check, timestamp tolerance to bound replay, provider event ID deduped before processing (webhook deliveries are always treated as at-least-once).
- **Client-provided prices, ownership, permissions, payment state, inspection state and vehicle history are never trusted.** The server derives all of these from its own records; client fields are hints at most, revalidated against the database and the state machine on every transition.

## Alternatives considered

**Adopt an OIDC provider (e.g. Keycloak, Ory Hydra) from day one.** Maximum conformance, JWKS rotation and federation for free. Rejected for Wave 1: it adds a stateful, security-critical service to deploy/operate/patch before the first job ships, and its session model fights the offline-first technician client. The issuer-agnostic abstraction buys the migration path at near-zero consumer cost. Revisit trigger: partner/B2B federation (Wave 7 developer platform) or MFA breadth — landing with an explicit follow-up ADR.

**Long-lived opaque API tokens instead of JWTs.** Simpler, instantly revocable. Rejected as the primary mechanism: every service call becomes a stateful lookup (latency, availability coupling to identity), and stateless claims are what let dispatch/notifications/payments scale independently. Opaque tokens remain the model for refresh and for partner API keys (Wave 7), which are revocation-first by nature.

**ABAC/policy-engine authorization (e.g. OPA) now.** Rejected at this stage: the domain's authz is well-expressed by RBAC plus resource ownership; a policy engine adds a runtime dependency and an authorization-decision audit problem before there is policy complexity to manage. The enumerated matrix is designed to be liftable into a policy engine later if cross-cutting conditional rules (time, geography, device trust) accumulate.

**Float/decimal money in application layer.** Rejected outright — rounding drift across M-Pesa (integer KES cents), card providers and the ledger is a known corruption class. Integer minor units end to end.

## Consequences

**Positive**

- The platform JWT middleware contract is fixed now: Wave 1 identity implements behind it, and every context consumes tokens identically; the future OIDC swap is invisible to consumers.
- The RBAC matrix and tenant-scoping tests give security review concrete artifacts, and give QA deterministic denial cases instead of vibes.
- Role-partitioned DB access (ADR-0003) turns the AI/ledger boundary and cross-tenant access from policy into physics.
- Append-only audit records for privileged actions align the security story with the passport's tamper-evident trust claim.

**Negative / costs**

- HS256 now means a key-management rotation discipline (shared verification secret per environment) that RS256 would make asymmetric; mitigated by short TTLs, rotation runbooks, and the planned OIDC move.
- Two-layer enforcement (middleware + service) is redundant by design; it costs code and tests, and skipping the service-layer check is a recurring review target.
- Repository-level tenant scoping forbids the convenient "just query it" path for support tooling; support capabilities must be built as audited, permissioned flows instead.
- MFA and OIDC federation are deferred — an accepted, explicit gap requiring a follow-up ADR before Wave 7 partner access, tracked on the roadmap.
