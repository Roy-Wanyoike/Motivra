import { randomUUID as expoRandomUUID } from 'expo-crypto';

/**
 * RFC 4122 v4 UUID for idempotency keys and operation ids.
 *
 * Production path: `expo-crypto`'s `randomUUID` — cryptographically strong,
 * backed by the platform secure random generator (issue #55 switched this
 * from the foundation's Web-Crypto-only implementation). In vitest the
 * `expo-crypto` module is aliased to a pure test double (tests/stubs), which
 * is the ONLY place a non-native implementation is allowed.
 *
 * Fallbacks, in order:
 *  1. `globalThis.crypto.randomUUID` (Node >= 19 runtimes);
 *  2. `globalThis.crypto.getRandomValues` (Web Crypto, still CSPRNG);
 *  3. hard failure — idempotency keys MUST be unique per logical operation,
 *     so we refuse to emit weak randomness rather than degrade silently.
 */
export function uuidv4(): string {
  try {
    if (typeof expoRandomUUID === 'function') {
      return expoRandomUUID();
    }
  } catch {
    // Native module unavailable in this runtime — fall through to Web Crypto.
  }

  const cryptoObj = globalThis.crypto as Crypto | undefined;
  if (cryptoObj && typeof cryptoObj.randomUUID === 'function') {
    return cryptoObj.randomUUID();
  }
  if (cryptoObj && typeof cryptoObj.getRandomValues === 'function') {
    const bytes = new Uint8Array(16);
    cryptoObj.getRandomValues(bytes);
    // Per RFC 4122 §4.4: set version 4 and variant bits.
    bytes[6] = (bytes[6]! & 0x0f) | 0x40;
    bytes[8] = (bytes[8]! & 0x3f) | 0x80;
    const hex = Array.from(bytes, (b) => b.toString(16).padStart(2, '0')).join('');
    return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
  }
  throw new Error('uuidv4: no secure randomness source available (expo-crypto and Web Crypto both unavailable)');
}
