import type { OutboxOperation } from './types';

/**
 * Persistence boundary for the outbox. The in-memory implementation below is
 * the test/reference adapter; the production adapter is expo-sqlite (a
 * documented follow-up — see apps/technician/README.md honesty table).
 *
 * Contract notes for any adapter:
 *  - All methods are async so SQLite (or AsyncStorage) implementations fit.
 *  - `seq` is assigned by the adapter on append and must be monotonic.
 *  - Ops in state IN_FLIGHT are NOT returned by listDue (lease semantics);
 *    use reclaimStaleInFlight to recover them after a crash/timeout.
 */
export interface OutboxStorageAdapter {
  append(op: Omit<OutboxOperation, 'seq'>): Promise<OutboxOperation>;
  get(id: string): Promise<OutboxOperation | null>;
  getByIdempotencyKey(key: string): Promise<OutboxOperation | null>;
  /** Due = PENDING with next_attempt_at <= nowMs, oldest seq first. */
  listDue(nowMs: number, limit: number): Promise<OutboxOperation[]>;
  /**
   * All ops in the given state, newest seq first (used by the sync
   * reconciler's re-basing step to find settled DONE/FAILED intents).
   */
  listByState(state: OutboxOperation['state'], limit: number): Promise<OutboxOperation[]>;
  update(op: OutboxOperation): Promise<void>;
  /** Counts by state — drives the UI badge. */
  countByState(): Promise<Record<OutboxOperation['state'], number>>;
  /**
   * Recovery: move IN_FLIGHT ops whose lease started before `olderThanMs` back
   * to PENDING (crash between marking IN_FLIGHT and writing the result).
   * Returns how many ops were reclaimed.
   */
  reclaimStaleInFlight(nowMs: number, leaseMs: number): Promise<number>;
}

/**
 * In-memory adapter (also the fake used by the vitest suite). Arrays are kept
 * in insertion order; `seq` starts at 1.
 */
export class InMemoryOutboxStorage implements OutboxStorageAdapter {
  private byId = new Map<string, OutboxOperation>();
  private seqCounter = 0;

  async append(op: Omit<OutboxOperation, 'seq'>): Promise<OutboxOperation> {
    if (this.byId.has(op.id)) {
      throw new Error(`outbox: duplicate operation id ${op.id}`);
    }
    for (const existing of this.byId.values()) {
      if (existing.idempotency_key === op.idempotency_key && existing.state !== 'FAILED') {
        throw new Error(
          `outbox: idempotency key ${op.idempotency_key} already used by operation ${existing.id}`,
        );
      }
    }
    this.seqCounter += 1;
    const stored: OutboxOperation = { ...op, seq: this.seqCounter };
    this.byId.set(stored.id, stored);
    return stored;
  }

  async get(id: string): Promise<OutboxOperation | null> {
    return this.byId.get(id) ?? null;
  }

  async getByIdempotencyKey(key: string): Promise<OutboxOperation | null> {
    for (const op of this.byId.values()) {
      if (op.idempotency_key === key) return op;
    }
    return null;
  }

  async listDue(nowMs: number, limit: number): Promise<OutboxOperation[]> {
    const due = [...this.byId.values()]
      .filter(
        (op) =>
          op.state === 'PENDING' && Date.parse(op.next_attempt_at) <= nowMs,
      )
      .sort((a, b) => a.seq - b.seq);
    return due.slice(0, limit);
  }

  async update(op: OutboxOperation): Promise<void> {
    if (!this.byId.has(op.id)) {
      throw new Error(`outbox: unknown operation ${op.id}`);
    }
    this.byId.set(op.id, { ...op });
  }

  async listByState(
    state: OutboxOperation['state'],
    limit: number,
  ): Promise<OutboxOperation[]> {
    return [...this.byId.values()]
      .filter((op) => op.state === state)
      .sort((a, b) => b.seq - a.seq) // newest first
      .slice(0, limit);
  }

  async countByState(): Promise<Record<OutboxOperation['state'], number>> {
    const counts: Record<OutboxOperation['state'], number> = {
      PENDING: 0,
      IN_FLIGHT: 0,
      FAILED: 0,
      DONE: 0,
    };
    for (const op of this.byId.values()) counts[op.state] += 1;
    return counts;
  }

  async reclaimStaleInFlight(nowMs: number, leaseMs: number): Promise<number> {
    let reclaimed = 0;
    for (const op of this.byId.values()) {
      if (op.state !== 'IN_FLIGHT') continue;
      const leaseStart = Date.parse(op.next_attempt_at); // lease start is stored here
      if (Number.isNaN(leaseStart) || nowMs - leaseStart >= leaseMs) {
        op.state = 'PENDING';
        op.next_attempt_at = new Date(nowMs).toISOString();
        this.byId.set(op.id, op);
        reclaimed += 1;
      }
    }
    return reclaimed;
  }
}
