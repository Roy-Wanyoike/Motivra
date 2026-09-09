import type { JobLocalStore, JobSnapshot, JobTransitionRow } from '../sync/api';

/**
 * In-memory local read model for jobs. Implements the reconciler's
 * `JobLocalStore` interface and adds a tiny subscription API for React
 * (`useSyncExternalStore`).
 *
 * Production persistence (SQLite via expo-sqlite / AsyncStorage mirror) is a
 * documented follow-up — this store resets on app restart, which is exactly
 * the kind of claim the README honesty table makes explicitly.
 */
export class JobStore implements JobLocalStore {
  private jobs = new Map<string, JobSnapshot>();
  private pendingJobIds = new Set<string>();
  private listeners = new Set<() => void>();
  private snapshotCache: JobSnapshot[] = [];

  /** Called by the UI after enqueueing an outbox op for a job. */
  markPending(jobId: string, pending: boolean): void {
    if (pending) this.pendingJobIds.add(jobId);
    else this.pendingJobIds.delete(jobId);
    this.emit();
  }

  /** Called by the UI after an op for the job reaches DONE/FAILED. */
  hasPendingOpsForJob(jobId: string): boolean {
    return this.pendingJobIds.has(jobId);
  }

  applyServerSnapshot(job: JobSnapshot): void {
    this.jobs.set(job.id, job);
    this.emit();
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
    for (const job of jobs) this.jobs.set(job.id, job);
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

  private emit(): void {
    for (const listener of this.listeners) listener();
  }
}
