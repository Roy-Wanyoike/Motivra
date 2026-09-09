import { computeBackoffDelayMs, DEFAULT_BACKOFF_POLICY, type BackoffPolicy } from './backoff';
import { InMemoryOutboxStorage, type OutboxStorageAdapter } from './storage';
import type {
  DispatchResult,
  DrainReport,
  EnqueueInput,
  EnqueueResult,
  OutboxDispatcher,
  OutboxOperation,
} from './types';
import { uuidv4 } from '../lib/uuid';

export interface OutboxDeps {
  storage: OutboxStorageAdapter;
  dispatcher: OutboxDispatcher;
  /** Wall clock in epoch ms; injectable for deterministic tests. */
  clock?: () => number;
  /** Random source for jitter; injectable for deterministic tests. */
  rng?: () => number;
  backoff?: BackoffPolicy;
  /** Lease duration for IN_FLIGHT reclaim, ms. */
  leaseMs?: number;
}

/**
 * Offline-first mutation outbox.
 *
 * ## Idempotency contract (directive: outbox + idempotency rules)
 *
 * 1. **Enqueue-time dedupe** — `enqueue` with an idempotency key that already
 *    maps to a live (non-FAILED) operation returns that operation with
 *    `deduped: true`; a second row is never created.
 * 2. **At-most-once application per key** — the dispatcher MUST send
 *    `idempotency_key` with every attempt. Draining the same operation twice
 *    (crash between server-apply and local DONE write, lease reclaim, manual
 *    retry) re-sends the SAME key, and the server applies it once. The test
 *    suite simulates exactly this crash window and asserts a single apply.
 * 3. **Replay safety is server-side** — the outbox never "proves" an op was
 *    not applied before marking DONE; DONE is only an optimisation. Replay is
 *    always safe by construction.
 *
 * ## Retry policy
 * Retryable failures re-arm with `computeBackoffDelayMs` (exponential + jitter,
 * capped). Permanent rejections (4xx validation / 409 conflict) park the op as
 * FAILED without retry — the server is authoritative and retrying an illegal
 * move cannot succeed.
 */
export class Outbox {
  readonly storage: OutboxStorageAdapter;
  private readonly dispatcher: OutboxDispatcher;
  private readonly clock: () => number;
  private readonly rng: () => number;
  private readonly backoff: BackoffPolicy;
  private readonly leaseMs: number;
  private drainInProgress = false;

  constructor(deps: OutboxDeps) {
    this.storage = deps.storage;
    this.dispatcher = deps.dispatcher;
    this.clock = deps.clock ?? Date.now;
    this.rng = deps.rng ?? Math.random;
    this.backoff = deps.backoff ?? DEFAULT_BACKOFF_POLICY;
    this.leaseMs = deps.leaseMs ?? 60_000;
  }

  /**
   * Queue a mutation for background delivery. If an operation with the same
   * idempotency key exists and is not FAILED, it is returned unchanged
   * (`deduped: true`) — callers can enqueue the same logical mutation freely
   * (e.g. a user tapping "Accept job" twice while offline).
   */
  async enqueue(input: EnqueueInput): Promise<EnqueueResult> {
    const key = input.idempotencyKey ?? uuidv4();
    const existing = await this.storage.getByIdempotencyKey(key);
    if (existing && existing.state !== 'FAILED') {
      return { operation: existing, deduped: true };
    }
    const nowMs = this.clock();
    const operation = await this.storage.append({
      id: uuidv4(),
      type: input.type,
      payload: input.payload,
      idempotency_key: key,
      created_at: new Date(nowMs).toISOString(),
      attempts: 0,
      next_attempt_at: new Date(nowMs).toISOString(),
      state: 'PENDING',
      last_error: null,
    });
    return { operation, deduped: false };
  }

  /**
   * Drain due operations. Safe to call concurrently (second caller reports
   * skipped). Steps:
   *   1. reclaim stale IN_FLIGHT leases (crash recovery),
   *   2. pick due PENDING ops (oldest first),
   *   3. per op: mark IN_FLIGHT → dispatch → write terminal state.
   */
  async drain(limit = 20): Promise<DrainReport> {
    const report: DrainReport = {
      processed: 0,
      succeeded: 0,
      failedPermanent: 0,
      failedRetryable: 0,
      skippedInFlight: 0,
    };
    if (this.drainInProgress) {
      // Single-writer discipline per Outbox instance; a concurrent caller is a
      // no-op rather than a queue (the next sync tick will pick the work up).
      report.skippedInFlight = -1; // sentinel: drain itself was skipped
      return report;
    }
    this.drainInProgress = true;
    try {
      const nowMs = this.clock();
      await this.storage.reclaimStaleInFlight(nowMs, this.leaseMs);
      const due = await this.storage.listDue(nowMs, limit);
      for (const op of due) {
        report.processed += 1;
        await this.attemptOne(op);
        if (op.state === 'DONE') report.succeeded += 1;
        else if (op.state === 'FAILED') report.failedPermanent += 1;
        else report.failedRetryable += 1;
      }
      return report;
    } finally {
      this.drainInProgress = false;
    }
  }

  private async attemptOne(op: OutboxOperation): Promise<void> {
    const nowMs = this.clock();
    // Mark IN_FLIGHT (lease start in next_attempt_at) before dispatching so a
    // crash mid-dispatch is recoverable via reclaimStaleInFlight.
    op.state = 'IN_FLIGHT';
    op.next_attempt_at = new Date(nowMs).toISOString();
    await this.storage.update(op);

    let result: DispatchResult;
    try {
      result = await this.dispatcher.dispatch(op);
    } catch (err) {
      result = {
        kind: 'retryable-failure',
        reason: err instanceof Error ? err.message : String(err),
      };
    }

    const afterMs = this.clock();
    switch (result.kind) {
      case 'accepted': {
        op.state = 'DONE';
        op.last_error = null;
        break;
      }
      case 'rejected': {
        op.state = 'FAILED'; // permanent; surfaced in UI, never retried
        op.last_error = result.reason;
        break;
      }
      case 'retryable-failure': {
        op.attempts += 1;
        op.last_error = result.reason;
        if (op.attempts >= this.backoff.maxAttempts) {
          op.state = 'FAILED'; // dead-letter after exhausting attempts
        } else {
          op.state = 'PENDING';
          const delay = computeBackoffDelayMs(op.attempts, this.backoff, this.rng);
          op.next_attempt_at = new Date(afterMs + delay).toISOString();
        }
        break;
      }
    }
    await this.storage.update(op);
  }

  /** Pending (not yet DONE/FAILED) count for the offline badge. */
  async pendingCount(): Promise<number> {
    const counts = await this.storage.countByState();
    return counts.PENDING + counts.IN_FLIGHT;
  }
}

export { InMemoryOutboxStorage };
export type { OutboxStorageAdapter };
export type { DispatchResult, DrainReport, EnqueueResult, OutboxDispatcher, OutboxOperation };
