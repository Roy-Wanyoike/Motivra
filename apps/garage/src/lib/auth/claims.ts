/*
 * Access-token verification for Motivra Garage.
 *
 * Tokens are issued by the identity service (asymmetric EdDSA/RS256 with a
 * `kid` header, ADR-0007; HS256 exists only behind the explicit migration
 * flag). Garage verifies signature via the issuer's JWKS (public keys
 * only), then expiry (30s leeway, mirroring the platform validator),
 * issuer, audience and the claim shape every service expects: sub, roles,
 * exp. A token that fails ANY check is treated as absent — deny-by-default.
 */

import { sharedJwksCache, type JwksCache } from "./jwks";

export interface AccessClaims {
  sub: string;
  role: string;
  roles: string[];
  sid: string;
  tenantId: string;
  issuedAt: number;
  expiresAt: number;
}

export type VerifyResult =
  | { ok: true; claims: AccessClaims }
  | { ok: false; reason: string };

export interface VerifyOptions {
  jwksUrl: string;
  issuer: string;
  audience: string;
  /** Legacy HS256 acceptance (migration window only, ADR-0007). */
  allowHS256?: boolean;
  /** Shared secret for the legacy HS256 path. */
  hs256Secret?: string;
  /** Injectable cache (tests). Default: the shared per-URL cache. */
  jwksCache?: JwksCache;
  /** Injectable clock (tests) — epoch milliseconds. */
  now?: () => number;
}

/** Expiry leeway, mirroring backend/platform/jwt.go. */
const LEEWAY_MS = 30 * 1000;

function base64UrlDecode(part: string): Uint8Array {
  const normalized = part.replace(/-/g, "+").replace(/_/g, "/");
  const padded = normalized + "=".repeat((4 - (normalized.length % 4)) % 4);
  const binary = atob(padded);
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i += 1) {
    bytes[i] = binary.charCodeAt(i);
  }
  return bytes;
}

function decodeJson<T>(part: string): T | null {
  try {
    return JSON.parse(new TextDecoder().decode(base64UrlDecode(part))) as T;
  } catch {
    return null;
  }
}

function audienceMatches(aud: unknown, expected: string): boolean {
  if (typeof aud === "string") {
    return aud === expected;
  }
  if (Array.isArray(aud)) {
    return aud.some((entry) => entry === expected);
  }
  return false;
}

async function importVerificationKey(key: {
  kty: string;
  crv?: string;
  n?: string;
  e?: string;
  x?: string;
  y?: string;
}): Promise<CryptoKey> {
  const subtle = crypto.subtle;
  if (key.kty === "OKP" && key.crv === "Ed25519") {
    return subtle.importKey("jwk", key as JsonWebKey, { name: "Ed25519" }, false, ["verify"]);
  }
  if (key.kty === "RSA") {
    return subtle.importKey(
      "jwk",
      key as JsonWebKey,
      { name: "RSASSA-PKCS1-v1_5", hash: "SHA-256" },
      false,
      ["verify"],
    );
  }
  throw new Error(`unsupported JWK key type ${key.kty ?? "unknown"}`);
}

async function verifySignature(
  signingInput: string,
  signature: Uint8Array,
  header: { alg?: string; kid?: string },
  options: VerifyOptions,
): Promise<"verified" | string> {
  const encoder = new TextEncoder();
  const data = encoder.encode(signingInput);

  if (header.alg === "HS256") {
    if (!options.allowHS256 || options.hs256Secret === undefined || options.hs256Secret === "") {
      return "HS256 tokens are not accepted (asymmetric keys only, ADR-0007)";
    }
    const secret = encoder.encode(options.hs256Secret);
    const key = await crypto.subtle.importKey(
      "raw",
      secret,
      { name: "HMAC", hash: "SHA-256" },
      false,
      ["verify"],
    );
    const valid = await crypto.subtle.verify(
      { name: "HMAC", hash: "SHA-256" },
      key,
      signature as unknown as ArrayBuffer,
      data,
    );
    return valid ? "verified" : "HS256 signature check failed";
  }

  if (header.alg !== "EdDSA" && header.alg !== "RS256") {
    return `unexpected token algorithm ${header.alg ?? "missing"}`;
  }

  const kid = header.kid;
  if (typeof kid !== "string" || kid === "") {
    return "token header is missing the kid claim";
  }
  const cache = options.jwksCache ?? sharedJwksCache(options.jwksUrl);
  const jwk = await cache.getKey(kid);
  if (!jwk) {
    return `no verification key for kid "${kid}"`;
  }
  const cryptoKey = await importVerificationKey(jwk);
  const algorithm =
    header.alg === "EdDSA" ? { name: "Ed25519" } : { name: "RSASSA-PKCS1-v1_5", hash: "SHA-256" };
  const valid = await crypto.subtle.verify(
    algorithm,
    cryptoKey,
    signature as unknown as ArrayBuffer,
    data,
  );
  return valid ? "verified" : `${header.alg} signature check failed`;
}

/**
 * Verify one access token end to end. Every failure mode resolves to
 * `{ ok: false }` with a reason safe to log (no token material).
 */
export async function verifyAccessToken(
  token: string,
  options: VerifyOptions,
): Promise<VerifyResult> {
  const parts = token.split(".");
  if (parts.length !== 3 || parts.some((part) => part.length === 0)) {
    return { ok: false, reason: "malformed token" };
  }
  const header = decodeJson<{ alg?: string; kid?: string }>(parts[0]);
  const payload = decodeJson<Record<string, unknown>>(parts[1]);
  if (!header || !payload) {
    return { ok: false, reason: "undecodable token" };
  }

  let signature: Uint8Array;
  try {
    signature = base64UrlDecode(parts[2]);
  } catch {
    return { ok: false, reason: "undecodable signature" };
  }

  try {
    const outcome = await verifySignature(`${parts[0]}.${parts[1]}`, signature, header, options);
    if (outcome !== "verified") {
      return { ok: false, reason: outcome };
    }
  } catch {
    return { ok: false, reason: "signature verification failed" };
  }

  const now = (options.now ?? Date.now)();
  const exp = typeof payload.exp === "number" ? payload.exp : Number.NaN;
  if (!Number.isFinite(exp)) {
    return { ok: false, reason: "token has no expiry" };
  }
  if (exp * 1000 <= now - LEEWAY_MS) {
    return { ok: false, reason: "token expired" };
  }

  if (typeof payload.iss !== "string" || payload.iss !== options.issuer) {
    return { ok: false, reason: "issuer mismatch" };
  }
  if (!audienceMatches(payload.aud, options.audience)) {
    return { ok: false, reason: "audience mismatch" };
  }
  if (typeof payload.sub !== "string" || payload.sub === "") {
    return { ok: false, reason: "token has no subject" };
  }

  const roles = Array.isArray(payload.roles)
    ? payload.roles.filter((entry): entry is string => typeof entry === "string")
    : [];
  if (roles.length === 0) {
    // ADR-0004: verifiers refuse tokens with no roles claim.
    return { ok: false, reason: "token carries no roles" };
  }

  return {
    ok: true,
    claims: {
      sub: payload.sub,
      role: typeof payload.role === "string" ? payload.role : "",
      roles,
      sid: typeof payload.sid === "string" ? payload.sid : "",
      tenantId: typeof payload.tenant_id === "string" ? payload.tenant_id : "",
      issuedAt: typeof payload.iat === "number" ? payload.iat : 0,
      expiresAt: exp,
    },
  };
}
