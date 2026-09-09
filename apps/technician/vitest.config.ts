import { defineConfig } from 'vitest/config';

// Pure-logic tests only (outbox, sync, domain guards). Screens are typed by
// `tsc --noEmit` but not unit-tested here — rendering tests need a RN test
// renderer and are a documented follow-up (see apps/technician/README.md).
export default defineConfig({
  test: {
    environment: 'node',
    include: ['tests/**/*.test.ts'],
  },
});
