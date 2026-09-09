import { describe, expect, it } from 'vitest';

import { Outbox } from '../src/outbox/outbox';
import { InMemoryOutboxStorage } from '../src/outbox/storage';
import type { DispatchResult, OutboxDispatcher, OutboxOperation } from '../src/outbox/types';
import { SyncReconciler } from '../src/sync/reconciler';
import type { JobSnapshot, PullFn, PullPage } from '../src/sync/api';
import { JobStore } from '../src/state/job-store';
import { uuidv4 } from '../src/lib/uuid';

const JOB_A: JobSnapshot = {
  id: 'aaaaaaaa-1111-4111-8111-000000000001',
  status: 'ASSIGNED',
  problem_summary: 'Starter motor — CBD',
  technician_id: 'technician-1',
  vehicle_id: 'vehicle-1',
  created_at: '2026-09-09T08:00:00Z',
  updated_at: '2026-09-09T08:05:00Z',
  transitions: [],
};

class AcceptingServer implements OutboxDispatcher {
  readonly seenKeys: string[] = [];
  async dispatch(op: OutboxOperation): Promise<DispatchResult> {
    this.seenKeys.push(op.idempotency_key);
    return { kind: 'accepted' };
  }
}

describe('sync reconciler — enqueue → drain happy path', () => {
  it('pushes a queued job.transition, then pulls server state and advances the cursor', async () => {
    const server = new AcceptingServer();
    const outbox = new Outbox({
      storage: new InMemoryOutboxStorage(),
      dispatcher: server,
      clock: () => 1_000,
      rng: () => 0.5,
    });
    const store = new JobStore();
    store.seed([JOB_A]);

    const pages: Array<(cursor: string | null, limit: number) => PullPage> = [
      () => ({ jobs: [{ ...JOB_A, status: 'ACCEPTED', updated_at: '2026-09-09T08:10:00Z' }], nextCursor: 'cursor-1' }),
      () => ({ jobs: [{ ...JOB_A, status: 'EN_ROUTE', updated_at: '2026-09-09T08:15:00Z' }], nextCursor: null }),
    ];
    let pageCalls = 0;
    const pull: PullFn = async (cursor) => pages[pageCalls++]!(cursor ?? null, 50);

    const reconciler = new SyncReconciler({ outbox, pull, store, clock: () => 2_000 });

    // 1. Technician taps a legal transition offline: guard → outbox op → optimistic status.
    const { operation } = await outbox.enqueue({
      type: 'job.transition',
      payload: { to: 'ACCEPTED', reason: 'accepted on device' },
      idempotencyKey: uuidv4(),
    });
    expect(operation.state).toBe('PENDING');

    // 2. sync() = push (drain) + pull (cursor pages).
    const summary = await reconciler.sync();

    expect(summary.push.processed).toBe(1);
    expect(summary.push.succeeded).toBe(1);
    expect(summary.pulled).toBe(2);
    expect(summary.applied).toBe(2);
    expect(summary.skippedForConflict).toBe(0);
    expect(summary.cursor).toBeNull();
    expect((await outbox.storage.get(operation.id))?.state).toBe('DONE');

    // Server saw the op exactly once, with the idempotency key attached.
    expect(server.seenKeys).toHaveLength(1);
    expect(server.seenKeys[0]).toBe(operation.idempotency_key);

    // Local read model converged to the latest server snapshot.
    expect(store.get(JOB_A.id)?.status).toBe('EN_ROUTE');
  });

  it('second sync with no new work is a clean no-op', async () => {
    const server = new AcceptingServer();
    const outbox = new Outbox({
      storage: new InMemoryOutboxStorage(),
      dispatcher: server,
      clock: () => 0,
      rng: () => 0.5,
    });
    const store = new JobStore();
    const pull: PullFn = async () => ({ jobs: [], nextCursor: null });
    const reconciler = new SyncReconciler({ outbox, pull, store, clock: () => 0 });

    const first = await reconciler.sync();
    const second = await reconciler.sync();

    expect(first.push.processed).toBe(0);
    expect(second.push.processed).toBe(0);
    expect(server.seenKeys).toHaveLength(0);
    expect(second.pulled).toBe(0);
  });

  it('conflict placeholder: a job with live local intent is NOT clobbered by pull', async () => {
    const outbox = new Outbox({
      storage: new InMemoryOutboxStorage(),
      dispatcher: { dispatch: async () => ({ kind: 'accepted' }) },
      clock: () => 0,
      rng: () => 0.5,
    });
    const store = new JobStore();
    store.seed([{ ...JOB_A, status: 'ASSIGNED' }]);
    // Local intent pending for JOB_A (e.g. op queued, not yet drained).
    store.markPending(JOB_A.id, true);

    const serverSays: JobSnapshot = { ...JOB_A, status: 'CANCELLED' }; // server moved on
    const pull: PullFn = async () => ({ jobs: [serverSays], nextCursor: null });
    const reconciler = new SyncReconciler({ outbox, pull, store, clock: () => 0 });

    const summary = await reconciler.sync();
    expect(summary.skippedForConflict).toBe(1);
    expect(summary.pulled).toBe(1);
    // Optimistic local state preserved — no last-write-wins.
    expect(store.get(JOB_A.id)?.status).toBe('ASSIGNED');
  });

  it('JobStore implements the JobLocalStore contract the reconciler needs', () => {
    const store = new JobStore();
    store.seed([JOB_A]);
    expect(store.hasPendingOpsForJob(JOB_A.id)).toBe(false);
    store.markPending(JOB_A.id, true);
    expect(store.hasPendingOpsForJob(JOB_A.id)).toBe(true);
    store.applyServerSnapshot({ ...JOB_A, status: 'ACCEPTED' });
    expect(store.all()).toHaveLength(1);
    expect(store.get(JOB_A.id)?.status).toBe('ACCEPTED');
  });
});
