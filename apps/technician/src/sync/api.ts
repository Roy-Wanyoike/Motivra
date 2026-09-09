import type { JsonValue } from '../lib/json';

/**
 * Pull side of the reconciler.
 *
 * NOTE (honest scope): `contracts/jobs/openapi.yaml` today exposes
 * `GET /v1/jobs?status=` (dispatcher/admin listing) and
 * `GET /v1/jobs/{id}` — there is **no cursor-based delta endpoint yet**.
 * This interface is therefore declared against the sync contract Motivra
 * Tech needs (`GET /v1/jobs/sync?cursor=...` returning changed jobs +
 * an opaque cursor); wiring it to the real server is blocked on a contracts
 * change owned by the jobs context. Until then the app runs on mock data and
 * tests inject a fake implementation.
 */
export interface JobSnapshot {
  id: string;
  status: string;
  problem_summary: string;
  technician_id: string | null;
  vehicle_id: string | null;
  created_at: string;
  updated_at: string;
  /** Server transition log rows (append-only) for the detail timeline. */
  transitions?: JobTransitionRow[];
}

export interface JobTransitionRow {
  id: number;
  job_id: string;
  from_status: string;
  to_status: string;
  actor_id: string | null;
  reason: string | null;
  created_at: string;
}

export interface PullPage {
  jobs: JobSnapshot[];
  /** Opaque server cursor; null/undefined means "no more pages". */
  nextCursor: string | null;
}

export type PullFn = (cursor: string | null, limit: number) => Promise<PullPage>;

/**
 * Minimal local read model the reconciler applies pulled snapshots into.
 * The production implementation is a SQLite/AsyncStorage mirror (follow-up);
 * the in-memory implementation lives in src/state/job-store.ts.
 */
export interface JobLocalStore {
  /** Jobs currently known locally that have unfinished (PENDING/IN_FLIGHT) outbox ops. */
  hasPendingOpsForJob(jobId: string): boolean;
  /** Upsert a server snapshot into the local read model. */
  applyServerSnapshot(job: JobSnapshot): void;
  all(): JobSnapshot[];
}

/**
 * TODO(conflict-rules — build directive, ARCHITECTURE.md §6/§7):
 * The real reconciler must NOT use last-write-wins. Rules this skeleton
 * commits to (and the full implementation must enforce):
 *
 *  - **Server-authoritative fields** (never taken from the client):
 *    `status`, `technician_id`, timestamps, prices/estimates, payment state,
 *    inspection state, vehicle history entries. The client proposes; the
 *    jobs service decides.
 *  - **Client intent vs server state**: a pulled job whose id has a live
 *    outbox op is skipped here (kept local-optimistic) and re-reconciled on
 *    the next pull after the op settles. This is the placeholder for proper
 *    three-way merge (base / client-intent / server-state), where a rejected
 *    intent (409) is re-based on the server snapshot and re-presented to the
 *    technician — never silently overwritten.
 *  - **No mutation of append-only data**: transition logs and history rows are
 *    insert-only; conflicts on them are impossible by construction.
 */
export function shouldApplySnapshot(job: JobSnapshot, store: JobLocalStore): boolean {
  if (store.hasPendingOpsForJob(job.id)) {
    // Conflict-detection placeholder: do not clobber an optimistic local
    // state that still has in-flight intent. Revisit after the op drains.
    return false;
  }
  return true;
}
