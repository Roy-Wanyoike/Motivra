/*
 * JWKS fetch + cache for access-token verification.
 *
 * Command is a VERIFIER: it holds only public keys fetched from the
 * identity service's /.well-known/jwks.json (ADR-0007 posture — verifiers
 * hold no signing material). Keys are cached per URL with a TTL and a
 * bounded refetch on unknown `kid` (rotation-aware, mirroring the platform
 * Go jwksCache semantics in backend/platform/jwks.go).
 */

export interface JwkKey {
  kty: string;
  kid: string;
  alg?: string;
  crv?: string;
  n?: string;
  e?: string;
  x?: string;
  y?: string;
  use?: string;
  key_ops?: string[];
}

export type KeyFetcher = (jwksUrl: string) => Promise<JwkKey[]>;

export interface JwksCache {
  /** Resolve the JWK for a kid, refetching once on an unknown kid. */
  getKey(kid: string): Promise<JwkKey | null>;
  /** Test/diagnostic hook: drop every cached key set. */
  reset(): void;
}

export interface JwksCacheOptions {
  /** Cache TTL (default 5 minutes). */
  ttlMs?: number;
  /** Minimum gap between forced refetches for unknown kids (default 1s). */
  minRefreshGapMs?: number;
  /** Injectable fetcher (tests); default: global fetch + 5s timeout. */
  fetcher?: KeyFetcher;
  /** Injectable clock (tests). */
  now?: () => number;
}

const DEFAULT_TTL_MS = 5 * 60 * 1000;
const DEFAULT_REFRESH_GAP_MS = 1000;

async function defaultFetcher(jwksUrl: string): Promise<JwkKey[]> {
  const response = await fetch(jwksUrl, {
    cache: "no-store",
    signal: AbortSignal.timeout(5000),
  });
  if (!response.ok) {
    throw new Error(`jwks: endpoint answered HTTP ${response.status}`);
  }
  const body = (await response.json()) as { keys?: JwkKey[] };
  return Array.isArray(body.keys) ? body.keys : [];
}

export function createJwksCache(
  jwksUrl: string,
  options: JwksCacheOptions = {},
): JwksCache {
  const ttlMs = options.ttlMs ?? DEFAULT_TTL_MS;
  const minRefreshGapMs = options.minRefreshGapMs ?? DEFAULT_REFRESH_GAP_MS;
  const fetcher = options.fetcher ?? defaultFetcher;
  const now = options.now ?? Date.now;

  let keys = new Map<string, JwkKey>();
  let fetchedAt = 0;
  let lastRefetchAt = 0;
  let inflight: Promise<void> | null = null;

  async function refresh(): Promise<void> {
    const fetched = await fetcher(jwksUrl);
    const map = new Map<string, JwkKey>();
    for (const key of fetched) {
      if (typeof key?.kid === "string" && key.kid !== "") {
        map.set(key.kid, key);
      }
    }
    // Publish atomically: a failed fetch keeps the previous key set, so a
    // JWKS blip never blanks verification (keys stay usable until TTL).
    if (map.size > 0) {
      keys = map;
      fetchedAt = now();
    }
  }

  function ensureFresh(): Promise<void> {
    const age = now() - fetchedAt;
    if (fetchedAt > 0 && age < ttlMs) {
      return Promise.resolve();
    }
    if (inflight === null) {
      inflight = refresh().finally(() => {
        inflight = null;
      });
    }
    return inflight;
  }

  return {
    async getKey(kid: string): Promise<JwkKey | null> {
      await ensureFresh();
      const known = keys.get(kid);
      if (known) {
        return known;
      }
      // Unknown kid — the rotation signal. Refetch, but bounded: at most one
      // forced refetch per minRefreshGapMs so a flood of forged kids cannot
      // hammer the identity service.
      const sinceRefetch = now() - lastRefetchAt;
      if (lastRefetchAt === 0 || sinceRefetch >= minRefreshGapMs) {
        lastRefetchAt = now();
        await refresh().catch(() => undefined);
      }
      return keys.get(kid) ?? null;
    },
    reset(): void {
      keys = new Map();
      fetchedAt = 0;
      lastRefetchAt = 0;
      inflight = null;
    },
  };
}

/*
 * One cache per JWKS URL, module-level. Under the Node.js proxy runtime and
 * every route handler this lives for the process lifetime; each isolate
 * warms its own copy on first use.
 */
const caches = new Map<string, JwksCache>();

export function sharedJwksCache(jwksUrl: string): JwksCache {
  let cache = caches.get(jwksUrl);
  if (!cache) {
    cache = createJwksCache(jwksUrl);
    caches.set(jwksUrl, cache);
  }
  return cache;
}
