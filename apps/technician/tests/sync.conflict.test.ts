import { describe, expect, it } from 'vitest';

import { Outbox } from '../src/outbox/outbox';
import { InMemoryOutboxStorage } from '../src/outbox/storage';
import type { DispatchResult, OutboxDispatcher, OutboxOperation } from '../src/outbox/types';
import { SyncReconciler } from '../src/sync/reconciler';
import { threeWayMerge } from '../src/sync/merge';
import { mergeIncoming } from '../src/sync/api';
import type { JobSnapshot, PendingIntent, PullFn, PullPage } from '../src/sync/api';
import { JobStore } from '../src/state/job-store';

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

function intent(overrides: Partial<PendingIntent> = {}): PendingIntent {
  return {
    op_id: 'op-1',
    idempotency_key: 'key-1',
    to_status: 'ACCEPTED',
    enqueued_at: '2026-09-09T08:06:00Z',
    ...overrides,
  };
}

describe('threeWayMerge — pure rule matrix (issue #55)', () => {
  it('no live intent → adopt the server snapshot as-is', () => {
    const result = threeWayMerge(JOB_A, { ...JOB_A, status: 'CANCELLED' }, null);
    expect(result.kind).toBe('server-state');
  });

  it('intent created against the base and server unchanged → optimistic overlay', () => {
    const result = threeWayMerge(JOB_A, { ...JOB_A }, intent());
    expect(result.kind).toBe('intent-applied');
    if (result.kind === 'intent-applied') {
      // Overlay only the status; server fields stay authoritative.
      expect(result.snapshot.status).toBe('ACCEPTED');
      expect(result.snapshot.technician_id).toBe(JOB_A.technician_id);
      expect(result.snapshot.updated_at).toBe(JOB_A.updated_at);
      expect(result.snapshot.pending_intent?.op_id).toBe('op-1');
    }
  });

  it('server moved but the transition is still legal → overlay survives', () => {
    const server = { ...JOB_A, updated_at: '2026-09-09T08:20:00Z' }; // moved, still ASSIGNED
    const result = threeWayMerge(JOB_A, server, intent());
    expect(result.kind).toBe('intent-applied');
  });

  it('server moved into a contradicting state → conflict with the PURE server snapshot', () => {
    const server: JobSnapshot = { ...JOB_A, status: 'CANCELLED', updated_at: '2026-09-09T08:20:00Z' };
    const result = threeWayMerge(JOB_A, server, intent());
    expect(result.kind).toBe('conflict');
    if (result.kind === 'conflict') {
      // Never the client's wish — the display adopts the server state.
      expect(result.snapshot.status).toBe('CANCELLED');
      expect(result.snapshot.pending_intent).toBeUndefined();
      expect(result.reason).toContain('CANCELLED');
    }
  });

  it('intent already applied server-side → server-state (overlay dropped)', () => {
    const server: JobSnapshot = { ...JOB_A, status: 'ACCEPTED', updated_at: '2026-09-09T08:20:00Z' };
    const result = threeWayMerge(JOB_A, server, intent());
    expect(result.kind).toBe('server-state');
  });

  it('unknown status in the intent → conflict (defensive)', () => {
    const result = threeWayMerge(JOB_A, { ...JOB_A }, intent({ to_status: 'TELEPORTED' }));
    expect(result.kind).toBe('conflict');
    if (result.kind === 'conflict') {
      expect(result.reason).toContain('TELEPORTED');
    }
  });

  it('append-only transition rows are never merged destructively (insert-only overlay)', () => {
    // The merge only ever overlays `status` + `pending_intent`; the server's
    // transition array is carried through untouched.
    const server = { ...JOB_A, transitions: [{ id: 7, job_id: JOB_A.id, from_status: 'DISPATCHING', to_status: 'ASSIGNED', actor_id: 'd', reason: null, created_at: '2026-09-09T08:01:00Z' }] };
    const result = threeWayMerge(JOB_A, server, intent());
    expect(result.kind).toBe('intent-applied');
    if (result.kind === 'intent-applied') {
      expect(result.snapshot.transitions).toHaveLength(1);
      expect(result.snapshot.transitions![0]!.id).toBe(7);
    }
  });
});

describe('mergeIncoming — store-backed decision', () => {
  it('without an intent, adopts the server snapshot', () => {
    const store = new JobStore();
    const decision = mergeIncoming({ ...JOB_A, status: 'EN_ROUTE' }, store);
    expect(decision).toMatchObject({ adopt: true, outcome: 'server-state' });
  });

  it('with a live legal intent, returns the merged display snapshot', () => {
    const store = new JobStore();
    store.seed([JOB_A]);
    store.setPendingIntent(JOB_A.id, intent());
    const decision = mergeIncoming({ ...JOB_A }, store);
    expect(decision.outcome).toBe('intent-applied');
    expect(decision.snapshot?.status).toBe('ACCEPTED');
  });

  it('with a contradicting server move, returns the conflict surface', () => {
    const store = new JobStore();
    store.seed([JOB_A]);
    store.setPendingIntent(JOB_A.id, intent());
    const decision = mergeIncoming({ ...JOB_A, status: 'CANCELLED', updated_at: '2026-09-09T08:30:00Z' }, store);
    expect(decision.outcome).toBe('conflict');
    expect(decision.conflict?.proposed_status).toBe('ACCEPTED');
    expect(decision.conflict?.server_status).toBe('CANCELLED');
  });
});

class RejectingServer implements OutboxDispatcher {
  readonly seenKeys: string[] = [];
  async dispatch(op: OutboxOperation): Promise<DispatchResult> {
    this.seenKeys.push(op.idempotency_key);
    return { kind: 'rejected', reason: '409: illegal transition from CANCELLED' };
  }
}

class OfflineServer implements OutboxDispatcher {
  async dispatch(): Promise<DispatchResult> {
    return { kind: 'retryable-failure', reason: 'network unavailable' };
  }
}

describe('SyncReconciler — 409 re-basing end-to-end (issue #55)', () => {
  it('a server-rejected intent becomes a persistent rejected record; overlay dropped; server adopted', async () => {
    const server = new RejectingServer();
    const outbox = new Outbox({ storage: new InMemoryOutboxStorage(), dispatcher: server, clock: () => 1_000, rng: () => 0.5 });
    const store = new JobStore();
    store.seed([JOB_A]);

    const { operation } = await outbox.enqueue({
      type: 'job.transition',
      payload: { job_id: JOB_A.id, to: 'ACCEPTED', reason: 'proposed on device' },
      idempotencyKey: 'key-409',
    });
    store.setPendingIntent(JOB_A.id, {
      op_id: operation.id,
      idempotency_key: operation.idempotency_key,
      to_status: 'ACCEPTED',
      enqueued_at: '2026-09-09T08:06:00Z',
    });
    store.optimisticStatus(JOB_A.id, 'ACCEPTED');
    expect(store.get(JOB_A.id)?.status).toBe('ACCEPTED'); // optimistic before sync

    const serverSays: JobSnapshot = { ...JOB_A, status: 'CANCELLED', updated_at: '2026-09-09T08:30:00Z' };
    const pull: PullFn = async () => ({ jobs: [serverSays], nextCursor: null });
    const reconciler = new SyncReconciler({ outbox, pull, store, clock: () => 2_000 });

    const summary = await reconciler.sync();
    expect(summary.push.failedPermanent).toBe(1);
    expect(summary.rebased).toBe(1);

    // The optimistic overlay is gone; the server verdict is on display.
    expect(store.get(JOB_A.id)?.status).toBe('CANCELLED');
    expect(store.pendingIntentForJob(JOB_A.id)).toBeNull();

    // The contradiction is re-presented and persists (never silently overwritten).
    const rejected = store.rejectedIntents();
    expect(rejected).toHaveLength(1);
    expect(rejected[0]).toMatchObject({
      op_id: operation.id,
      idempotency_key: 'key-409',
      proposed_status: 'ACCEPTED',
    });
    expect(rejected[0]!.reason).toContain('409');
    expect(store.get(JOB_A.id)?.local_conflict?.proposed_status).toBe('ACCEPTED');
  });

  it('re-scanning FAILED ops does not duplicate rejected records (dedupe per op id)', async () => {
    const outbox = new Outbox({
      storage: new InMemoryOutboxStorage(),
      dispatcher: new RejectingServer(),
      clock: () => 1_000,
      rng: () => 0.5,
    });
    const store = new JobStore();
    store.seed([JOB_A]);
    const { operation } = await outbox.enqueue({
      type: 'job.transition',
      payload: { job_id: JOB_A.id, to: 'ACCEPTED' },
    });
    store.setPendingIntent(JOB_A.id, {
      op_id: operation.id, idempotency_key: operation.idempotency_key,
      to_status: 'ACCEPTED', enqueued_at: '2026-09-09T08:06:00Z',
    });
    const pull: PullFn = async () => ({ jobs: [JOB_A], nextCursor: null });
    const reconciler = new SyncReconciler({ outbox, pull, store, clock: () => 2_000 });

    await reconciler.sync();
    await reconciler.sync();
    await reconciler.sync();
    expect(store.rejectedIntents()).toHaveLength(1);
  });

  it('a successful (DONE) op releases its intent — the next pull adopts pure server state', async () => {
    const outbox = new Outbox({
      storage: new InMemoryOutboxStorage(),
      dispatcher: { dispatch: async () => ({ kind: 'accepted' }) },
      clock: () => 1_000,
      rng: () => 0.5,
    });
    const store = new JobStore();
    store.seed([JOB_A]);
    const { operation } = await outbox.enqueue({
      type: 'job.transition',
      payload: { job_id: JOB_A.id, to: 'ACCEPTED' },
    });
    store.setPendingIntent(JOB_A.id, {
      op_id: operation.id, idempotency_key: operation.idempotency_key,
      to_status: 'ACCEPTED', enqueued_at: '2026-09-09T08:06:00Z',
    });
    const accepted: JobSnapshot = { ...JOB_A, status: 'ACCEPTED', updated_at: '2026-09-09T08:10:00Z' };
    const pull: PullFn = async () => ({ jobs: [accepted], nextCursor: null });
    const reconciler = new SyncReconciler({ outbox, pull, store, clock: () => 2_000 });

    const summary = await reconciler.sync();
    expect(summary.rebased).toBe(1);
    expect(store.pendingIntentForJob(JOB_A.id)).toBeNull();
    expect(store.get(JOB_A.id)?.status).toBe('ACCEPTED');
    expect(store.get(JOB_A.id)?.pending_intent ?? null).toBeNull();
  });

  it('a live (not-yet-sent) intent overlays a moved-but-still-legal server state', async () => {
    const outbox = new Outbox({
      storage: new InMemoryOutboxStorage(),
      dispatcher: new OfflineServer(), // op stays PENDING — intent stays live
      clock: () => 1_000,
      rng: () => 0.5,
    });
    const store = new JobStore();
    store.seed([JOB_A]);
    const { operation } = await outbox.enqueue({
      type: 'job.transition',
      payload: { job_id: JOB_A.id, to: 'ACCEPTED' },
    });
    store.setPendingIntent(JOB_A.id, {
      op_id: operation.id, idempotency_key: operation.idempotency_key,
      to_status: 'ACCEPTED', enqueued_at: '2026-09-09T08:06:00Z',
    });
    // Server moved (different updated_at) but the job is still ASSIGNED.
    const moved: JobSnapshot = { ...JOB_A, technician_id: 'technician-9', updated_at: '2026-09-09T08:22:00Z' };
    const pull: PullFn = async () => ({ jobs: [moved], nextCursor: null });
    const reconciler = new SyncReconciler({ outbox, pull, store, clock: () => 2_000 });

    const summary = await reconciler.sync();
    expect(summary.mergedWithIntent).toBe(1);
    expect(summary.applied).toBe(0);
    // Server fields adopted; only status is the optimistic overlay.
    expect(store.get(JOB_A.id)?.status).toBe('ACCEPTED');
    expect(store.get(JOB_A.id)?.technician_id).toBe('technician-9');
    // The merge base stays the pure server snapshot.
    expect(store.serverBaseForJob(JOB_A.id)?.status).toBe('ASSIGNED');
  });

  it('an orphaned FAILED op (no job linkage) is skipped by re-basing without crashing', async () => {
    const outbox = new Outbox({
      storage: new InMemoryOutboxStorage(),
      dispatcher: new RejectingServer(),
      clock: () => 1_000,
      rng: () => 0.5,
    });
    const store = new JobStore();
    await outbox.enqueue({ type: 'job.transition', payload: { to: 'ACCEPTED' } }); // no job_id, no intent
    const pull: PullFn = async () => ({ jobs: [], nextCursor: null });
    const reconciler = new SyncReconciler({ outbox, pull, store, clock: () => 2_000 });

    const summary = await reconciler.sync();
    expect(summary.push.failedPermanent).toBe(1);
    expect(summary.rebased).toBe(0);
    expect(store.rejectedIntents()).toHaveLength(0);
  });
});

describe('JobStore — conflict surface lifecycle', () => {
  it('clearConflict removes the surface; the rejected-intent log keeps the record', () => {
    const store = new JobStore();
    store.seed([JOB_A]);
    store.recordRejectedIntent(JOB_A.id, {
      op_id: 'op-x', idempotency_key: 'key-x', proposed_status: 'ACCEPTED',
      server_status: 'CANCELLED', reason: '409', at: '2026-09-09T09:00:00Z',
    });
    expect(store.get(JOB_A.id)?.local_conflict).not.toBeNull();
    store.clearConflict(JOB_A.id);
    expect(store.get(JOB_A.id)?.local_conflict ?? null).toBeNull();
    expect(store.rejectedIntents()).toHaveLength(1);
  });
});

describe('InMemoryOutboxStorage.listByState', () => {
  it('returns only ops in the requested state, newest seq first', async () => {
    const storage = new InMemoryOutboxStorage();
    const mk = (id: string, key: string) => ({
      id, type: 'job.transition', payload: {}, idempotency_key: key,
      created_at: '2026-09-09T08:00:00Z', attempts: 0,
      next_attempt_at: '2026-09-09T08:00:00Z', state: 'PENDING' as const, last_error: null,
    });
    const a = await storage.append(mk('a', 'ka'));
    const b = await storage.append(mk('b', 'kb'));
    await storage.append(mk('c', 'kc'));
    await storage.update({ ...a, state: 'FAILED' });
    await storage.update({ ...b, state: 'FAILED' });

    const failed = await storage.listByState('FAILED', 10);
    expect(failed.map((op) => op.id)).toEqual(['b', 'a']); // newest first
    const limited = await storage.listByState('FAILED', 1);
    expect(limited.map((op) => op.id)).toEqual(['b']);
  });
});
