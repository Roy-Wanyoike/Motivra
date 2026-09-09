import { describe, expect, it } from 'vitest';

import { Outbox } from '../src/outbox/outbox';
import { InMemoryOutboxStorage } from '../src/outbox/storage';
import type { DispatchResult, OutboxDispatcher, OutboxOperation } from '../src/outbox/types';
import { uuidv4 } from '../src/lib/uuid';

/** Deterministic clock: advance manually. */
class FakeClock {
  private now = 1_000_000;
  nowMs = (): number => this.now;
  advance = (ms: number): void => {
    this.now += ms;
  };
}

const NO_JITTER_RNG = () => 0.5; // symmetric jitter factor collapses to 0 with ratio

/** Recording dispatcher used to assert exactly what left the outbox. */
class RecordingDispatcher implements OutboxDispatcher {
  readonly dispatched: OutboxOperation[] = [];
  constructor(private readonly result: (op: OutboxOperation, call: number) => DispatchResult) {}
  async dispatch(op: OutboxOperation): Promise<DispatchResult> {
    this.dispatched.push(op);
    return this.result(op, this.dispatched.length);
  }
}

function makeOutbox(clock: FakeClock, dispatcher: OutboxDispatcher): Outbox {
  return new Outbox({
    storage: new InMemoryOutboxStorage(),
    dispatcher,
    clock: clock.nowMs,
    rng: NO_JITTER_RNG,
  });
}

describe('outbox idempotency', () => {
  it('enqueue with the same idempotency key twice returns the same op (deduped), never two rows', async () => {
    const clock = new FakeClock();
    const dispatcher = new RecordingDispatcher(() => ({ kind: 'accepted' }));
    const outbox = makeOutbox(clock, dispatcher);

    const first = await outbox.enqueue({
      type: 'job.transition',
      payload: { to: 'ACCEPTED' },
      idempotencyKey: 'key-1',
    });
    const second = await outbox.enqueue({
      type: 'job.transition',
      payload: { to: 'ACCEPTED' },
      idempotencyKey: 'key-1',
    });

    expect(first.deduped).toBe(false);
    expect(second.deduped).toBe(true);
    expect(second.operation.id).toBe(first.operation.id);
    expect(await outbox.storage.getByIdempotencyKey('key-1')).not.toBeNull();
  });

  it('enqueue auto-generates distinct UUIDv4 keys when omitted', async () => {
    const clock = new FakeClock();
    const outbox = makeOutbox(clock, new RecordingDispatcher(() => ({ kind: 'accepted' })));
    const a = await outbox.enqueue({ type: 'job.transition', payload: {} });
    const b = await outbox.enqueue({ type: 'job.transition', payload: {} });
    expect(a.operation.idempotency_key).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/);
    expect(b.operation.idempotency_key).not.toBe(a.operation.idempotency_key);
  });

  it('CRASH REPLAY: op applied server-side, client crashes before DONE, re-drain re-sends same key — server applies ONCE', async () => {
    const clock = new FakeClock();
    // Server that applies at-most-once per idempotency key (the contract the
    // jobs service implements; mirrored here to prove client replay safety).
    const serverAppliedPayloads: unknown[] = [];
    const serverKeys = new Set<string>();
    class IdempotentServer implements OutboxDispatcher {
      async dispatch(op: OutboxOperation): Promise<DispatchResult> {
        if (serverKeys.has(op.idempotency_key)) {
          return { kind: 'accepted' }; // replay: deduped server-side, no second apply
        }
        serverKeys.add(op.idempotency_key);
        serverAppliedPayloads.push(op.payload);
        // Simulate crash AFTER apply, BEFORE the client can record DONE.
        throw new Error('device battery died mid-flight');
      }
    }
    const outbox = makeOutbox(clock, new IdempotentServer());

    const { operation } = await outbox.enqueue({
      type: 'job.transition',
      payload: { to: 'ACCEPTED' },
      idempotencyKey: 'crash-key',
    });

    // First drain: server applies, client crashes → op recoverable, not lost, not DONE.
    const first = await outbox.drain();
    expect(first.processed).toBe(1);
    expect(serverAppliedPayloads).toHaveLength(1);
    const afterCrash = await outbox.storage.get(operation.id);
    expect(afterCrash?.state).not.toBe('DONE');

    // Re-drain after lease reclaim: SAME key goes out again…
    clock.advance(10 * 60_000);
    const second = await outbox.drain();
    expect(second.processed).toBe(1);

    // …and the server still has exactly ONE application for that logical op.
    expect(serverAppliedPayloads).toHaveLength(1);
    expect(serverKeys.size).toBe(1);

    // Final drain completes the DONE write (no crash this time).
    clock.advance(10 * 60_000);
    await outbox.drain();
    expect((await outbox.storage.get(operation.id))?.state).toBe('DONE');
    expect(serverAppliedPayloads).toHaveLength(1); // still once
    expect(await outbox.pendingCount()).toBe(0);
  });

  it('draining a fully DONE outbox dispatches nothing new', async () => {
    const clock = new FakeClock();
    const dispatcher = new RecordingDispatcher(() => ({ kind: 'accepted' }));
    const outbox = makeOutbox(clock, dispatcher);
    await outbox.enqueue({ type: 'job.transition', payload: { to: 'ACCEPTED' }, idempotencyKey: 'k' });
    await outbox.drain();
    const countAfterFirst = dispatcher.dispatched.length;
    clock.advance(60_000);
    await outbox.drain();
    expect(dispatcher.dispatched.length).toBe(countAfterFirst);
    expect(await outbox.pendingCount()).toBe(0);
  });

  it('duplicate operation ids are rejected by storage (schema integrity)', async () => {
    const storage = new InMemoryOutboxStorage();
    const base = {
      id: uuidv4(),
      type: 'job.transition',
      payload: {},
      idempotency_key: 'dup-key',
      created_at: new Date(0).toISOString(),
      attempts: 0,
      next_attempt_at: new Date(0).toISOString(),
      state: 'PENDING' as const,
      last_error: null,
    };
    await storage.append(base);
    await expect(storage.append({ ...base, idempotency_key: 'other-key' })).rejects.toThrow(/duplicate operation id/);
  });

  it('storage rejects a second live op with the same idempotency key', async () => {
    const storage = new InMemoryOutboxStorage();
    const base = {
      id: uuidv4(),
      type: 'job.transition',
      payload: {},
      idempotency_key: 'same-key',
      created_at: new Date(0).toISOString(),
      attempts: 0,
      next_attempt_at: new Date(0).toISOString(),
      state: 'PENDING' as const,
      last_error: null,
    };
    await storage.append(base);
    await expect(storage.append({ ...base, id: uuidv4() })).rejects.toThrow(/idempotency key/);
  });
});
