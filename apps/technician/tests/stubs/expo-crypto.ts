/**
 * Vitest double for `expo-crypto` (aliased in vitest.config.ts).
 *
 * The production app resolves the real native module; tests run in Node,
 * where `globalThis.crypto.randomUUID` (Node >= 19) provides the same
 * contract. This is the only place a non-native UUID implementation is
 * permitted (issue #55).
 */
export function randomUUID(): string {
  const cryptoObj = globalThis.crypto as Crypto | undefined;
  if (cryptoObj && typeof cryptoObj.randomUUID === 'function') {
    return cryptoObj.randomUUID();
  }
  throw new Error('expo-crypto stub: globalThis.crypto.randomUUID unavailable in this test runtime');
}
