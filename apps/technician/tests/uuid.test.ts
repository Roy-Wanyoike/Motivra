import { describe, expect, it } from 'vitest';

import { uuidv4 } from '../src/lib/uuid';

const V4_PATTERN = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;

describe('uuidv4 — secure randomness (issue #55)', () => {
  it('emits RFC 4122 v4 format with correct version and variant bits', () => {
    for (let i = 0; i < 100; i += 1) {
      expect(uuidv4()).toMatch(V4_PATTERN);
    }
  });

  it('is unique across a large sample (idempotency-key requirement)', () => {
    const seen = new Set<string>();
    for (let i = 0; i < 1_000; i += 1) {
      seen.add(uuidv4());
    }
    expect(seen.size).toBe(1_000);
  });
});
