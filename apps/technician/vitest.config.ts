import { resolve } from 'node:path';
import { defineConfig } from 'vitest/config';

// Pure-logic tests only (outbox, sync, merge, domain guards). Screens are
// typed by `tsc --noEmit` but not unit-tested here — rendering tests need a
// RN test renderer and are a documented follow-up (see apps/technician/README.md).
export default defineConfig({
  resolve: {
    alias: {
      // Production uuid.ts imports the native expo-crypto module; tests run
      // in Node, so it is aliased to a pure double (issue #55).
      'expo-crypto': resolve(__dirname, 'tests/stubs/expo-crypto.ts'),
    },
  },
  test: {
    environment: 'node',
    include: ['tests/**/*.test.ts'],
  },
});
