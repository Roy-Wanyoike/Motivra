import { describe, expect, it } from 'vitest';

import { computeBackoffDelayMs, DEFAULT_BACKOFF_POLICY } from '../src/outbox/backoff';
import type { BackoffPolicy } from '../src/outbox/backoff';
import { Outbox } from '../src/outbox/outbox';
import { InMemoryOutboxStorage } from '../src/outbox/storage';
import type { OutboxDispatcher, OutboxOperation } from '../src/outbox/types';

const POLICY: BackoffPolicy = {
  baseDelayMs: 1_000,
  factor: 2,
  maxDelayMs: 30_000,
  jitterRatio: 0,
  maxAttempts: 4,
};

/** Deterministic rng: deterministic "random" values in [0,1). */
function seqRng(values: number[]): () => number {
  let i = 0;
  return () => values[i++ % values.length]!;
}

describe('backoff schedule', () => {
  it('doubles per attempt: 1s, 2s, 4s, 8s, 16s, 30s(capped at maxDelay)', () => {
    const rng = seqRng([0.5]); // jitterRatio 0 → rng irrelevant
    expect(computeBackoffDelayMs(1, POLICY, rng)).toBe(1_000);
    expect(computeBackoffDelayMs(2, POLICY, rng)).toBe(2_000);
    expect(computeBackoffDelayMs(3, POLICY, rng)).toBe(4_000);
    expect(computeBackoffDelayMs(4, POLICY, rng)).toBe(8_000);
    expect(computeBackoffDelayMs(5, POLICY, rng)).toBe(16_000);
    expect(computeBackoffDelayMs(6, POLICY, rng)).toBe(30_000);
    expect(computeBackoffDelayMs(20, POLICY, rng)).toBe(30_000); // stays capped
  });

  it('applies symmetric jitter within ±jitterRatio of the capped delay', () => {
    const policy: BackoffPolicy = { ...POLICY, jitterRatio: 0.25 };
    // rng=0 → delay*(1-0.25); rng=1 → delay*(1+0.25); rng=0.5 → exact delay.
    expect(computeBackoffDelayMs(3, policy, seqRng([0]))).toBe(3_000);
    expect(computeBackoffDelayMs(3, policy, seqRng([1]))).toBe(5_000);
    expect(computeBackoffDelayMs(3, policy, seqRng([0.5]))).toBe(4_000);
    // Many draws must stay inside the jitter band.
    for (let i = 0; i < 200; i++) {
      const d = computeBackoffDelayMs(3, policy);
      expect(d).toBeGreaterThanOrEqual(3_000);
      expect(d).toBeLessThanOrEqual(5_000);
    }
  });

  it('clamps negative/zero attempts to the base delay', () => {
    expect(computeBackoffDelayMs(0, POLICY, seqRng([0.5]))).toBe(1_000);
    expect(computeBackoffDelayMs(-5, POLICY, seqRng([0.5]))).toBe(1_000);
  });

  it('drain re-arms retryable failures on the backoff schedule and dead-letters after maxAttempts', async () => {
    const now = { value: 1_000_000 };
    const clock = () => now.value;
    const rng = seqRng([0.5]);

    const alwaysFails: OutboxDispatcher = {
      dispatch: async () => ({ kind: 'retryable-failure', reason: 'network unreachable' }),
    };
    const outbox = new Outbox({
      storage: new InMemoryOutboxStorage(),
      dispatcher: alwaysFails,
      clock,
      rng,
      backoff: POLICY,
    });

    const { operation } = await outbox.enqueue({ type: 'job.transition', payload: { to: 'ACCEPTED' } });
    let op: OutboxOperation | null = operation;

    // Attempt 1 → re-armed +1s; attempt 2 → +2s; attempt 3 → +4s; attempt 4 → FAILED.
    const expectedDelays = [1_000, 2_000, 4_000];
    for (const delay of expectedDelays) {
      await outbox.drain();
      op = await outbox.storage.get(operation.id);
      expect(op?.state).toBe('PENDING');
      expect(op?.last_error).toBe('network unreachable');
      const nextAt = Date.parse(op!.next_attempt_at);
      expect(nextAt - now.value).toBe(delay);
      now.value += delay + 1;
    }

    await outbox.drain();
    op = await outbox.storage.get(operation.id);
    expect(op?.state).toBe('FAILED'); // dead-lettered after maxAttempts
    expect(await outbox.pendingCount()).toBe(0);
  });

  it('permanent rejections (4xx/409) park the op as FAILED immediately', async () => {
    const rejected: OutboxDispatcher = {
      dispatch: async () => ({ kind: 'rejected', reason: '409 illegal transition' }),
    };
    const outbox = new Outbox({
      storage: new InMemoryOutboxStorage(),
      dispatcher: rejected,
      clock: () => 0,
      rng: seqRng([0.5]),
      backoff: POLICY,
    });
    const { operation } = await outbox.enqueue({ type: 'job.transition', payload: {} });
    await outbox.drain();
    const op = await outbox.storage.get(operation.id);
    expect(op?.state).toBe('FAILED');
    expect(op?.attempts).toBe(0); // not counted as retryable attempts
    expect(op?.last_error).toBe('409 illegal transition');
  });

  it('accepted ops land DONE on first drain', async () => {
    const ok: OutboxDispatcher = { dispatch: async () => ({ kind: 'accepted' }) };
    const outbox = new Outbox({
      storage: new InMemoryOutboxStorage(),
      dispatcher: ok,
      clock: () => 0,
      rng: seqRng([0.5]),
      backoff: POLICY,
    });
    const { operation } = await outbox.enqueue({ type: 'job.transition', payload: {} });
    const report = await outbox.drain();
    expect(report.succeeded).toBe(1);
    expect((await outbox.storage.get(operation.id))?.state).toBe('DONE');
  });

  it('default policy is exponential with cap and jitter (sanity bounds)', () => {
    const p = DEFAULT_BACKOFF_POLICY;
    expect(p.factor).toBe(2);
    expect(p.maxDelayMs).toBeGreaterThan(p.baseDelayMs);
    expect(p.jitterRatio).toBeGreaterThan(0);
    expect(p.jitterRatio).toBeLessThanOrEqual(1);
  });
});
