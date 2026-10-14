# ADR-0007: Asymmetric JWT Signing with JWKS (EdDSA/RS256), `kid` Rotation and the Verifier Posture

- **Status:** Accepted
- **Date:** 2026-10-14
- **Deciders:** Principal Architect (arch-01), per issue #87
- **Related:** ADR-0004 (security baseline — this ADR supersedes its HS256 signing decision), ADR-0005 (observability — `/readyz` JWKS probe, decision metrics), ADR-0001 (platform layout — signer/validator live in `backend/platform`), `docs/RUNBOOK.md` §8a (rotation procedure)

## Context

ADR-0004 shipped HS256 access tokens with the issuer-agnostic abstraction explicitly reserved as the migration path. That debt is now due: every verifying service holds the issuer's shared `MOTIVRA_JWT_SECRET`, so any compromised validator — payments, vehicles, dispatch — can mint valid `SUPER_ADMIN` tokens for the whole platform. There is no `kid`, no rotation story beyond "replace the secret everywhere and restart", and no JWKS. Separately, the in-process rate limiter grants each replica its own budget, and refresh sessions had no user-facing management surface. Issue #87 covers all three; this ADR fixes the signing decision.

## Decision

### Signing and verification

- **Identity signs with asymmetric keys: Ed25519/EdDSA by default, RSA/RS256 as the interop option.** The `JWTSigner` (in `backend/platform`) loads a PEM private key (PKCS#8 for Ed25519, PKCS#1/PKCS#8 for RSA), derives a deterministic `kid` (SHA-256 thumbprint of the public key; an explicit `MOTIVRA_JWT_KEY_ID` overrides for pre-provisioned kids), and stamps every token with it.
- **Verifiers hold PUBLIC key material only, selected by the token's `kid` header**, from two key sources: the issuer's JWKS endpoint (`/.well-known/jwks.json`) and/or a distributed public-key file (`MOTIVRA_JWT_PUBLIC_KEY_FILE`) for services that cannot reach identity. A validator built from public keys provably cannot sign — `HasPrivateKeyMaterial()` exposes the property and the test suite asserts it (`TestHS256OnlyAcceptedBehindExplicitConfig`, `TestJWKSValidatorEndToEnd`).
- **HS256 survives only behind an explicit migration flag** (`MOTIVRA_JWT_ALLOW_HS256`) and in the test posture. It is never a default, and a JWKS/public-key validator refuses HS256 tokens outright — method allow-list and key resolution both reject it.
- **Ephemeral (generated) signing keys are allowed only in development/test.** Staging and production require a provisioned `MOTIVRA_JWT_PRIVATE_KEY_FILE` and refuse to boot otherwise; a production verifier with no key source at all also refuses to boot. Misconfiguration fails boot, never quietly widens trust (loading a private key where a public key is expected is a boot error).

### JWKS distribution and caching

- The identity service serves its public key set at `/.well-known/jwks.json` — public by design, briefly cacheable, the only trust anchor verifiers need.
- Verifiers cache the set with: warm-up + probe at `/readyz`; unknown-`kid` refetch bounded to one fetch per second (an anti-stampede bound against forged-`kid` floods — the FIRST fetch bypasses the gap so a rotated-in key is picked up on first sighting); whole-set refresh every 5 minutes; and stale-serve during JWKS outages, with `/readyz` reporting the degraded feed. Availability beats a self-inflicted auth outage, but the operator sees it.

### Rotation

Two-phase, zero-downtime, documented in RUNBOOK §8a: (1) deploy the new signing key while the JWKS still serves BOTH kids — verifiers pick the new kid up via the bounded refetch; (2) after at most one access TTL (15 min default) every token in the wild is signed by the new key — retire the old kid from the key set. Old-key tokens fail at the next validation after retirement, i.e. **old tokens are invalid within one access TTL of the rotation's final step**, with no restarts and no shared secret to synchronize. For compromise, phase 1 is skipped: drop the compromised kid immediately.

### Cross-instance rate limiting and session management (same issue, adjacent decisions)

- When `MOTIVRA_REDIS_URL` is set, the auth limiter uses a Redis sliding window (atomic Lua over a ZSET) shared by all replicas — the global budget is enforced across instances, decisions fail open on Redis errors, and each decision is counted per backend/outcome/service. The in-process limiter remains the no-Redis fallback behind the same `AuthLimiter` interface.
- Refresh-session management becomes a user-facing API: `GET /v1/auth/sessions`, `DELETE /v1/auth/sessions/{id}`, `DELETE /v1/auth/sessions` (revoke-all) behind `RequireAuthenticated`, ownership-scoped, credential-free projections, audited (`identity.session.revoked`, `identity.session.revoked_all`).

## Alternatives considered

**Adopt an external OIDC provider now (Keycloak/Ory Hydra).** The ADR-0004 revisit trigger — partner federation or MFA breadth — has not fired. In-process asymmetric signing buys the security property (verifiers cannot forge) without adding a stateful, security-critical service to operate before Wave 7; the key-source validator keeps the OIDC swap a constructor change.

**Keep HS256 with a per-service verification secret.** No: it does not remove the forging capability, just shards it, and multiplies the rotation surface.

**Opaque-token introspection for access tokens (stateless → stateful).** Every service call gains a Redis/identity lookup and an availability coupling ADR-0004 explicitly rejected; opaque tokens stay where they belong — refresh and (later) partner API keys.

**miniredis-style in-test only for the distributed limiter.** Rejected as the sole proof: the cross-instance property must run against real Redis semantics (Lua + ZSET expiry); the suite gates on `TEST_REDIS_URL` (RUNBOOK §2a) and skips cleanly where no Redis exists.

## Consequences

**Positive**

- The "any validator can mint SUPER_ADMIN" class is closed and mechanically provable: verifiers hold public keys, and the test suite pins the posture (refuses HS256, `HasPrivateKeyMaterial()==false`, JWKS end-to-end, rotation within one access TTL, unknown-kid anti-stampede).
- Key rotation becomes a routine, zero-downtime RUNBOOK operation with bounded old-token lifetime instead of a synchronized-secret restart event.
- Rate limiting is honest at scale: one global budget across replicas, with fail-open availability and per-decision metrics.
- Users can see and revoke their own sessions; revocation is audited like other security-relevant actions.

**Negative / costs**

- Key management is now real operational work: a private-key file to provision and protect, `kid`s to track, and a rotation runbook that must actually be followed (§8a).
- JWKS adds a runtime dependency for verifiers (bounded: stale-serve + readyz degradation); a wholly offline verifier must use the distributed public-key file instead.
- The migration window keeps two token schemes alive; `MOTIVRA_JWT_ALLOW_HS256` must be retired (and with it the shared secret's signing role) — tracked as a follow-up once all deployments issue asymmetric tokens.
- Redis becomes load-bearing for consistent throttling (fail-open when absent) — a new failure mode to watch, mitigated by metrics and the existing Redis in the stack.
