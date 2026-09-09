import type {
  JobLocalStore,
  JobSnapshot,
  JobTransitionRow,
  LocalConflict,
  PendingIntent,
  RejectedIntent,
} from '../sync/api';

/**
 * In-memory local read model for jobs. Implements the reconciler's
 * `JobLocalStore` interface and adds a tiny subscription API for React
 * (`useSyncExternalStore`).
 *
 * Three maps back the conflict protocol (issue #55):
 *  - `serverBase` — last PURE server snapshot per job (the merge base);
 *  - `intents` / `intentsByOp` — live client intents (forward + reverse index);
 *  - `rejected` — persistent server-rejected intents, deduped per op id,
 *    kept visible on the display until acknowledged.
 *
 * Production persistence (SQLite via expo-sqlite / AsyncStorage mirror) is a
 * documented follow-up — this store resets on app restart, which is exactly
 * the kind of claim the README honesty table makes explicitly.
 */
export class JobStore implements JobLocalStore {
  private jobs = new Map<string, JobSnapshot>();
  private serverBase = new Map<string, JobSnapshot>();
  private pendingJobIds = new Set<string>();
  private intents = new Map<string, PendingIntent>();
  private intentsByOp = new Map<string, string>();
  private rejected = new Map<string, RejectedIntent>();
  private listeners = new Set<() => void>();
  private snapshotCache: JobSnapshot[] = [];

  /** Called by the UI after enqueueing an outbox op for a job. */
  markPending(jobId: string, pending: boolean): void {
    if (pending) this.pendingJobIds.add(jobId);
    else this.pendingJobIds.delete(jobId);
    this.emit();
  }

  /** Jobs currently known locally that have unfinished (PENDING/IN_FLIGHT) outbox ops. */
  hasPendingOpsForJob(jobId: string): boolean {
    return this.pendingJobIds.has(jobId);
  }

  pendingIntentForJob(jobId: string): PendingIntent | null {
    return this.intents.get(jobId) ?? null;
  }

  setPendingIntent(jobId: string, intent: PendingIntent | null): void {
    const previous = this.intents.get(jobId);
    if (previous) this.intentsByOp.delete(previous.op_id);
    if (!intent) {
      this.intents.delete(jobId);
      this.dropOverlay(jobId);
    } else {
      this.intents.set(jobId, intent);
      this.intentsByOp.set(intent.op_id, jobId);
      // Keep the display immediately consistent with the live intent.
      this.applyOverlay(jobId, intent);
    }
    this.emit();
  }

  jobIdForOp(opId: string): string | null {
    return this.intentsByOp.get(opId) ?? null;
  }

  serverBaseForJob(jobId: string): JobSnapshot | null {
    return this.serverBase.get(jobId) ?? null;
  }

  /** Upsert a PURE server snapshot: replaces the base and the display. */
  applyServerSnapshot(job: JobSnapshot): void {
    // An unacknowledged conflict surface survives server updates — the
    // technician must acknowledge it; a pull never silently hides a 409.
    const existingConflict = this.jobs.get(job.id)?.local_conflict ?? null;
    this.serverBase.set(job.id, { ...job, pending_intent: null, local_conflict: null });
    this.jobs.set(job.id, { ...job, pending_intent: null, local_conflict: existingConflict });
    this.emit();
  }

  /** Adopt a merged display snapshot (server fields + live intent overlay). */
  applyMergedSnapshot(server: JobSnapshot, display: JobSnapshot): void {
    this.serverBase.set(server.id, { ...server, pending_intent: null });
    this.jobs.set(server.id, display);
    this.emit();
  }

  applyLocalConflict(jobId: string, conflict: LocalConflict): void {
    const current = this.jobs.get(jobId);
    if (!current) return;
    this.jobs.set(jobId, { ...current, local_conflict: conflict });
    this.emit();
  }

  clearConflict(jobId: string): void {
    const current = this.jobs.get(jobId);
    if (!current || !current.local_conflict) return;
    const { local_conflict: _dropped, ...rest } = current;
    this.jobs.set(jobId, { ...rest, local_conflict: null });
    this.emit();
  }

  recordRejectedIntent(jobId: string, entry: RejectedIntent): void {
    // Dedupe per op id — the rebaser re-scans FAILED ops on every sync.
    if (this.rejected.has(entry.op_id)) return;
    this.rejected.set(entry.op_id, entry);
    this.applyLocalConflict(jobId, entry);
  }

  rejectedIntents(): RejectedIntent[] {
    return [...this.rejected.values()];
  }

  all(): JobSnapshot[] {
    return [...this.jobs.values()];
  }

  get(jobId: string): JobSnapshot | undefined {
    return this.jobs.get(jobId);
  }

  /** Optimistic local status update after a guarded transition is enqueued. */
  optimisticStatus(jobId: string, status: string): void {
    const job = this.jobs.get(jobId);
    if (!job) return;
    this.jobs.set(jobId, { ...job, status });
    this.emit();
  }

  /** Append a locally-proposed transition row to the detail timeline. */
  appendPendingTransition(jobId: string, row: JobTransitionRow): void {
    const job = this.jobs.get(jobId);
    if (!job) return;
    this.jobs.set(jobId, {
      ...job,
      transitions: [...(job.transitions ?? []), row],
    });
    this.emit();
  }

  seed(jobs: JobSnapshot[]): void {
    for (const job of jobs) {
      this.jobs.set(job.id, job);
      this.serverBase.set(job.id, { ...job, pending_intent: null });
    }
    this.emit();
  }

  subscribe = (listener: () => void): (() => void) => {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  };

  getSnapshot = (): JobSnapshot[] => {
    this.snapshotCache = [...this.jobs.values()];
    return this.snapshotCache;
  };

  /** Re-apply (or first-apply) the optimistic overlay for a live intent. */
  private applyOverlay(jobId: string, intent: PendingIntent): void {
    const base = this.serverBase.get(jobId);
    if (!base) return; // nothing pulled yet — display stays as-is until first pull
    this.jobs.set(jobId, { ...base, status: intent.to_status, pending_intent: intent });
  }

  /** Drop the optimistic overlay; display reverts to the pure server snapshot. */
  private dropOverlay(jobId: string): void {
    const base = this.serverBase.get(jobId);
    const current = this.jobs.get(jobId);
    if (!base || !current) return;
    if (current.status !== base.status || current.pending_intent) {
      this.jobs.set(jobId, { ...base });
    }
  }

  private emit(): void {
    for (const listener of this.listeners) listener();
  }
}
