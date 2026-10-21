/*
 * Access-token verification matrix — the deny-by-default core that turns
 * cookies into sessions (issue #123: anonymous callers must never be able
 * to synthesize a session, and non-staff tokens must not pass).
 *
 * Runs in the Node environment (WebCrypto Ed25519, ADR-0007): a keypair is
 * generated per test, the verifier gets a JWKS cache stub holding ONLY the
 * public JWK — mirroring the real posture where Command holds public keys
 * and nothing else.
 */

// @vitest-environment node
import { describe, expect, it } from "vitest";
import { verifyAccessToken, type VerifyOptions } from "@/lib/auth/claims";
import type { JwkKey, JwksCache } from "@/lib/auth/jwks";

const b64url = (value: object): string =>
  Buffer.from(JSON.stringify(value), "utf8").toString("base64url");

function cacheWith(key: JwkKey | null): JwksCache {
  return { getKey: async () => key, reset: () => {} };
}

async function ed25519Jwks(): Promise<{
  jwk: JwkKey;
  privateKey: CryptoKey;
  otherPrivateKey: CryptoKey;
}> {
  const a = await crypto.subtle.generateKey("Ed25519", true, ["sign", "verify"]);
  const b = await crypto.subtle.generateKey("Ed25519", true, ["sign", "verify"]);
  const jwk = (await crypto.subtle.exportKey("jwk", a.publicKey)) as JwkKey;
  return { jwk, privateKey: a.privateKey, otherPrivateKey: b.privateKey };
}

async function optionsWith(jwk: JwkKey | null, overrides: Partial<VerifyOptions> = {}) {
  return {
    jwksUrl: "https://identity.test/.well-known/jwks.json",
    issuer: "motivra-identity",
    audience: "motivra",
    jwksCache: cacheWith(jwk),
    ...(overrides as Partial<VerifyOptions>),
  } satisfies VerifyOptions;
}

interface SignArgs {
  privateKey: CryptoKey;
  header?: Record<string, unknown>;
  payload?: Record<string, unknown>;
}

async function signedToken({ privateKey, header, payload }: SignArgs): Promise<string> {
  const h = b64url(header ?? { alg: "EdDSA", kid: "test-key-1", typ: "JWT" });
  const p = b64url(
    payload ?? {
      sub: "0c2f6b7e-0000-4000-8000-000000000001",
      role: "DISPATCHER",
      roles: ["DISPATCHER"],
      sid: "sess-1",
      tenant_id: "",
      iat: 1_700_000_000,
      exp: 2_000_000_000,
      iss: "motivra-identity",
      aud: "motivra",
    },
  );
  const signature = await crypto.subtle.sign("Ed25519", privateKey, new TextEncoder().encode(`${h}.${p}`));
  return `${h}.${p}.${Buffer.from(signature).toString("base64url")}`;
}

describe("verifyAccessToken — deny-by-default matrix", () => {
  it("verifies a well-formed EdDSA token and maps the claim shape", async () => {
    const { jwk, privateKey } = await ed25519Jwks();
    const token = await signedToken({ privateKey });
    const result = await verifyAccessToken(token, await optionsWith(jwk));
    expect(result.ok).toBe(true);
    if (result.ok) {
      expect(result.claims.sub).toBe("0c2f6b7e-0000-4000-8000-000000000001");
      expect(result.claims.roles).toEqual(["DISPATCHER"]);
      expect(result.claims.role).toBe("DISPATCHER");
    }
  });

  it("rejects a token signed by a key the JWKS does not hold", async () => {
    const { jwk, otherPrivateKey } = await ed25519Jwks();
    const token = await signedToken({ privateKey: otherPrivateKey });
    const result = await verifyAccessToken(token, await optionsWith(jwk));
    expect(result.ok).toBe(false);
    if (!result.ok) expect(result.reason).toBe("EdDSA signature check failed");
  });

  it("rejects unknown kids and malformed tokens outright", async () => {
    const { jwk, privateKey } = await ed25519Jwks();
    const token = await signedToken({ privateKey });
    const unknownKid = await verifyAccessToken(token, await optionsWith(null));
    expect(unknownKid.ok).toBe(false);

    for (const garbage of ["", "a.b", "a.b.c", "...", "not-a-token"]) {
      const result = await verifyAccessToken(garbage, await optionsWith(jwk));
      expect(result.ok, garbage).toBe(false);
    }
  });

  it("refuses HS256 unless the explicit migration flag + secret are set (ADR-0007)", async () => {
    const secret = new TextEncoder().encode("shared-secret-migration-window");
    const key = await crypto.subtle.importKey("raw", secret, { name: "HMAC", hash: "SHA-256" }, false, [
      "sign",
    ]);
    const h = b64url({ alg: "HS256", typ: "JWT" });
    const p = b64url({ sub: "s", roles: ["SUPER_ADMIN"], exp: 2_000_000_000, iss: "motivra-identity", aud: "motivra" });
    const signature = await crypto.subtle.sign("HMAC", key, new TextEncoder().encode(`${h}.${p}`));
    const token = `${h}.${p}.${Buffer.from(signature).toString("base64url")}`;

    const refused = await verifyAccessToken(token, await optionsWith(null));
    expect(refused.ok).toBe(false);
    if (!refused.ok) expect(refused.reason).toContain("ADR-0007");

    const accepted = await verifyAccessToken(
      token,
      await optionsWith(null, { allowHS256: true, hs256Secret: "shared-secret-migration-window" }),
    );
    expect(accepted.ok).toBe(true);
  });

  it("rejects wrong issuer, audience, missing expiry and role-less tokens", async () => {
    const { jwk, privateKey } = await ed25519Jwks();
    const opts = await optionsWith(jwk);
    const base = { sub: "s", roles: ["ADMIN"], exp: 2_000_000_000, iat: 1_700_000_000, sid: "s1", tenant_id: "" };

    const wrongIssuer = await signedToken({ privateKey, payload: { ...base, iss: "other", aud: "motivra" } });
    expect((await verifyAccessToken(wrongIssuer, opts)).ok).toBe(false);

    const wrongAudience = await signedToken({ privateKey, payload: { ...base, iss: "motivra-identity", aud: "elsewhere" } });
    expect((await verifyAccessToken(wrongAudience, opts)).ok).toBe(false);

    const noExpiry = await signedToken({ privateKey, payload: { ...base, iss: "motivra-identity", aud: "motivra", exp: undefined } });
    expect((await verifyAccessToken(noExpiry, opts)).ok).toBe(false);

    const noRoles = await signedToken({
      privateKey,
      payload: { ...base, iss: "motivra-identity", aud: "motivra", roles: [], role: "CUSTOMER" },
    });
    const result = await verifyAccessToken(noRoles, opts);
    expect(result.ok).toBe(false);
    if (!result.ok) expect(result.reason).toBe("token carries no roles");
  });

  it("enforces expiry with the 30s platform leeway (clock injectable)", async () => {
    const { jwk, privateKey } = await ed25519Jwks();
    const base = { sub: "s", roles: ["ADMIN"], iss: "motivra-identity", aud: "motivra", iat: 1_700_000_000, sid: "s1", tenant_id: "" };
    const now = 2_000_000_000_000; // epoch ms
    const withinLeeway = await signedToken({ privateKey, payload: { ...base, exp: (now - 10_000) / 1000 } });
    const justExpired = await signedToken({ privateKey, payload: { ...base, exp: (now - 31_000) / 1000 } });
    const opts = await optionsWith(jwk, { now: () => now });

    expect((await verifyAccessToken(withinLeeway, opts)).ok).toBe(true);
    const expired = await verifyAccessToken(justExpired, opts);
    expect(expired.ok).toBe(false);
    if (!expired.ok) expect(expired.reason).toBe("token expired");
  });
});
