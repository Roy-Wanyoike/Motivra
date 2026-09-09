import type { Outbox } from '../outbox/outbox';
import type { DrainReport } from '../outbox/types';
import type { JobLocalStore, JobSnapshot, LocalConflict, PullFn } from './api';
import { mergeIncoming } from './api';

export interface SyncDeps {
  outbox: Outbox;
  pull: PullFn;
  store: JobLocalStore;
  clock?: () => number;
  /** Page size for pull; mirrors the contract's max limit of 200. */
  pageSize?: number;
  /** Max ops re-based per sync cycle (FAILED intents surfaced to the technician). */
  rebaseLimit?: number;
}

export interface SyncSummary {
  push: DrainReport;
  pulled: number;
  applied: number;
  /** Pulled jobs whose display carries a live, still-legal intent overlay. */
  mergedWithIntent: number;
  /** Pulled jobs whose server state contradicted the live intent. */
  conflicts: number;
  /** FAILED intents re-based into persistent rejected-intent records this cycle. */
  rebased: number;
  cursor: string | null;
  /** Epoch ms of the completed sync. */
  completedAt: number;
}

/**
 * Sync reconciler — push, then re-base, then pull.
 *
 * 1. **PUSH** — drain the outbox (idempotent replay; backoff handled inside).
 *    The server applies each op at-most-once per idempotency key.
 * 2. **RE-BASE** — ops the server permanently rejected (409 conflict /
 *    validation) are converted into persistent rejected-intent records on
 *    the local store: the technician is re-presented with the contradiction
 *    (proposed status, server verdict) and must acknowledge it. The
 *    optimistic overlay is dropped — the server's verdict is authoritative
 *    and is never silently overwritten (ARCHITECTURE.md §7). Ops that
 *    succeeded simply release their intent.
 * 3. **PULL** — fetch server deltas since the cursor and run each job
 *    through the three-way merge (`mergeIncoming`): no live intent → adopt
 *    server state; live intent still legal from the new server state →
 *    optimistic overlay; contradicting server state → adopt server state
 *    AND surface a conflict on the display until acknowledged.
 *
 * The loop is cursor-based and stops when the server reports no next page.
 */
export class SyncReconciler {
  private readonly outbox: Outbox;
  private readonly pull: PullFn;
  private readonly store: JobLocalStore;
  private readonly clock: () => number;
  private readonly pageSize: number;
  private readonly rebaseLimit: number;
  private syncInProgress = false;

  constructor(deps: SyncDeps) {
    this.outbox = deps.outbox;
    this.pull = deps.pull;
    this.store = deps.store;
    this.clock = deps.clock ?? Date.now;
    this.pageSize = deps.pageSize ?? 50;
    this.rebaseLimit = deps.rebaseLimit ?? 100;
  }

  /** Run one full push→re-base→pull cycle. Safe to call concurrently (second call is a no-op). */
  async sync(startCursor: string | null = null): Promise<SyncSummary> {
    if (this.syncInProgress) {
      throw new Error('sync: already in progress');
    }
    this.syncInProgress = true;
    try {
      // 1. PUSH — drain the outbox.
      const push = await this.outbox.drain();

      // 2. RE-BASE — settle intents whose ops reached a terminal state.
      const rebased = await this.rebaseSettledOps();

      // 3. PULL — converge the local read model through the three-way merge.
      let cursor = startCursor;
      let pulled = 0;
      let applied = 0;
      let mergedWithIntent = 0;
      let conflicts = 0;
      for (;;) {
        const page = await this.pull(cursor, this.pageSize);
        for (const job of page.jobs) {
          pulled += 1;
          const decision = mergeIncoming(job, this.store);
          if (!decision.adopt || !decision.snapshot) continue; // defensive; merge always adopts
          switch (decision.outcome) {
            case 'server-state':
              this.store.applyServerSnapshot(decision.snapshot);
              applied += 1;
              break;
            case 'intent-applied':
              this.store.applyMergedSnapshot(job, decision.snapshot);
              mergedWithIntent += 1;
              break;
            case 'conflict': {
              // Server state is adopted; the contradiction is surfaced and
              // stays visible until the technician acknowledges it.
              this.store.applyServerSnapshot(decision.snapshot);
              const conflict: LocalConflict = decision.conflict!;
              this.store.applyLocalConflict(job.id, conflict);
              conflicts += 1;
              break;
            }
          }
        }
        cursor = page.nextCursor;
        if (!cursor) break;
      }

      return {
        push,
        pulled,
        applied,
        mergedWithIntent,
        conflicts,
        rebased,
        cursor,
        completedAt: this.clock(),
      };
    } finally {
      this.syncInProgress = false;
    }
  }

  /**
   * Re-base settled outbox intents:
   *  - DONE ops release their intent (the next pull adopts pure server state);
   *  - FAILED `job.transition` ops become persistent rejected-intent records
   *    (deduped per op id) and their optimistic overlay is dropped.
   * Returns how many intents were settled/re-based.
   */
  private async rebaseSettledOps(): Promise<number> {
    let settled = 0;
    for (const state of ['DONE', 'FAILED'] as const) {
      const ops = await this.outbox.storage.listByState(state, this.rebaseLimit);
      for (const op of ops) {
        if (op.type !== 'job.transition') continue;
        const jobId = jobIdFromOp(op) ?? this.store.jobIdForOp(op.id);
        if (!jobId) continue; // orphaned op — no job linkage, nothing to re-base
        if (state === 'DONE') {
          this.store.setPendingIntent(jobId, null);
          settled += 1;
          continue;
        }
        // FAILED — permanent server rejection (409 conflict or validation).
        const payload = op.payload as { to?: unknown } | null;
        const proposed = typeof payload?.to === 'string' ? payload.to : 'unknown';
        this.store.setPendingIntent(jobId, null); // drop the optimistic overlay first
        this.store.recordRejectedIntent(jobId, {
          op_id: op.id,
          idempotency_key: op.idempotency_key,
          proposed_status: proposed,
          server_status: 'rejected by server',
          reason: op.last_error ?? 'server rejected the proposed transition (409)',
          at: new Date(this.clock()).toISOString(),
        });
        settled += 1;
      }
    }
    return settled;
  }
}

/** Extract the job id from a transition op payload (job_id is metadata added at enqueue). */
function jobIdFromOp(op: { payload: unknown }): string | null {
  if (op.payload && typeof op.payload === 'object' && !Array.isArray(op.payload)) {
    const candidate = (op.payload as Record<string, unknown>)['job_id'];
    if (typeof candidate === 'string' && candidate.length > 0) return candidate;
  }
  return null;
}

/** Re-exported for callers that want the snapshot type without importing api.ts. */
export type { JobSnapshot };
