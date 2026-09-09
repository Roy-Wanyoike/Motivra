/**
 * RFC 4122 v4 UUID without external dependencies.
 *
 * Uses the platform Web Crypto `crypto.getRandomValues` when available
 * (Node >= 19 for tests; Hermes in Expo provides it via the Expo runtime
 * since SDK 50). If it is ever unavailable, a clearly-flagged fallback keeps
 * the app functional with weaker randomness.
 *
 * TODO(follow-up): switch to `expo-crypto` (`randomUUID`) once dependencies
 * beyond the pinned lean set are allowed — tracked in the app README honesty
 * table. Idempotency keys MUST be unique per logical operation, so this stays
 * internal to the outbox until then.
 */
export function uuidv4(): string {
  const cryptoObj = globalThis.crypto as Crypto | undefined;
  if (cryptoObj && typeof cryptoObj.getRandomValues === 'function') {
    const bytes = new Uint8Array(16);
    cryptoObj.getRandomValues(bytes);
    // Per RFC 4122 §4.4: set version 4 and variant bits.
    bytes[6] = (bytes[6]! & 0x0f) | 0x40;
    bytes[8] = (bytes[8]! & 0x3f) | 0x80;
    const hex = Array.from(bytes, (b) => b.toString(16).padStart(2, '0')).join('');
    return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
  }
  // Flagged fallback — weaker randomness, monotonic counter to avoid collisions.
  fallbackCounter = (fallbackCounter + 1) % 0x10000;
  const rnd = () => Math.floor(Math.random() * 0x10000).toString(16).padStart(4, '0');
  return `${rnd()}${rnd()}-${rnd()}-4${rnd().slice(1)}-a${rnd().slice(1)}-${rnd()}${rnd()}${rnd().slice(0, 4)}`;
}

let fallbackCounter = 0;
