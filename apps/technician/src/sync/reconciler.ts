import type { Outbox } from '../outbox/outbox';
import type { DrainReport } from '../outbox/types';
import type { JobLocalStore, PullFn } from './api';
import { shouldApplySnapshot } from './api';

export interface SyncDeps {
  outbox: Outbox;
  pull: PullFn;
  store: JobLocalStore;
  clock?: () => number;
  /** Page size for pull; mirrors the contract's max limit of 200. */
  pageSize?: number;
}

export interface SyncSummary {
  push: DrainReport;
  pulled: number;
  applied: number;
  /** Jobs skipped by the conflict placeholder (live local ops). */
  skippedForConflict: number;
  cursor: string | null;
  /** Epoch ms of the completed sync. */
  completedAt: number;
}

/**
 * Sync reconciler skeleton — push, then pull.
 *
 * Push (drain the outbox) runs first so server state reflects local intent
 * before we read; pull then converges the local read model. The loop is
 * cursor-based and stops when the server reports no next page.
 *
 * Server-authoritative state handling: the local read model only ever adopts
 * a server snapshot when no live local intent exists for that job (see
 * `shouldApplySnapshot`); status and timestamps always come from the server
 * once adopted. This is a skeleton — the full conflict protocol (three-way
 * merge, intent re-basing on 409) is TODO per the build directive's conflict
 * rules and is tracked in the module docs of src/sync/api.ts.
 */
export class SyncReconciler {
  private readonly outbox: Outbox;
  private readonly pull: PullFn;
  private readonly store: JobLocalStore;
  private readonly clock: () => number;
  private readonly pageSize: number;
  private syncInProgress = false;

  constructor(deps: SyncDeps) {
    this.outbox = deps.outbox;
    this.pull = deps.pull;
    this.store = deps.store;
    this.clock = deps.clock ?? Date.now;
    this.pageSize = deps.pageSize ?? 50;
  }

  /** Run one full push→pull cycle. Safe to call concurrently (second call is a no-op). */
  async sync(startCursor: string | null = null): Promise<SyncSummary> {
    if (this.syncInProgress) {
      throw new Error('sync: already in progress');
    }
    this.syncInProgress = true;
    try {
      // 1. PUSH — drain the outbox (idempotent replay; backoff handled inside).
      const push = await this.outbox.drain();

      // 2. PULL — fetch server deltas since the cursor and apply them.
      let cursor = startCursor;
      let pulled = 0;
      let applied = 0;
      let skippedForConflict = 0;
      for (;;) {
        const page = await this.pull(cursor, this.pageSize);
        for (const job of page.jobs) {
          pulled += 1;
          if (shouldApplySnapshot(job, this.store)) {
            this.store.applyServerSnapshot(job);
            applied += 1;
          } else {
            skippedForConflict += 1;
          }
        }
        cursor = page.nextCursor;
        if (!cursor) break;
      }

      return {
        push,
        pulled,
        applied,
        skippedForConflict,
        cursor,
        completedAt: this.clock(),
      };
    } finally {
      this.syncInProgress = false;
    }
  }
}
