import type { JsonValue } from '../lib/json';
import { threeWayMerge } from './merge';

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
  /** Live client intent overlaying the display snapshot (never persisted server-side). */
  pending_intent?: PendingIntent | null;
  /** Unacknowledged conflict surface — stays visible until clearConflict(). */
  local_conflict?: LocalConflict | null;
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
  /** Live intent for the job, or null when no op is in flight. */
  pendingIntentForJob(jobId: string): PendingIntent | null;
  /** Attach/clear the live intent for a job (null clears it and drops the overlay). */
  setPendingIntent(jobId: string, intent: PendingIntent | null): void;
  /** Reverse lookup op id → job id (used by 409 re-basing). */
  jobIdForOp(opId: string): string | null;
  /** Last pure server snapshot (the merge "base"), or null if never pulled. */
  serverBaseForJob(jobId: string): JobSnapshot | null;
  /** Upsert a pure server snapshot into the local read model. */
  applyServerSnapshot(job: JobSnapshot): void;
  /** Adopt a merged display snapshot alongside its pure server base. */
  applyMergedSnapshot(server: JobSnapshot, display: JobSnapshot): void;
  /** Attach an unacknowledged conflict surface to the displayed snapshot. */
  applyLocalConflict(jobId: string, conflict: LocalConflict): void;
  /** Technician acknowledged the conflict — remove the surface. */
  clearConflict(jobId: string): void;
  /** Record a server-rejected intent (deduped per op id); surfaces it on the display. */
  recordRejectedIntent(jobId: string, entry: RejectedIntent): void;
  /** All rejected-intent records (append-only log, oldest first). */
  rejectedIntents(): RejectedIntent[];
  all(): JobSnapshot[];
}

/**
 * Live client intent for one job (created when a `job.transition` op is
 * enqueued into the outbox). Field names are snake_case to mirror the
 * outbox columns so a SQLite mirror can store them side by side.
 */
export interface PendingIntent {
  /** Outbox operation id that carries this intent. */
  op_id: string;
  idempotency_key: string;
  /** Proposed target status — the only field a client intent may propose. */
  to_status: string;
  /** ISO-8601 timestamp of intent creation. */
  enqueued_at: string;
}

/**
 * A server-rejected (409) or state-contradicted intent, re-presented to the
 * technician and kept visible until explicitly acknowledged (never silently
 * overwritten — ARCHITECTURE.md §7). Records are deduped per op id and kept
 * for the lifetime of the local store.
 */
export interface RejectedIntent {
  op_id: string;
  idempotency_key: string;
  /** Status the client proposed. */
  proposed_status: string;
  /** Server status at the time the conflict was surfaced (best known). */
  server_status: string;
  /** Human-readable rejection reason from the server verdict. */
  reason: string;
  /** ISO-8601 timestamp of the rejection/contradiction. */
  at: string;
}

/** Alias for the conflict surface attached to a displayed snapshot. */
export type LocalConflict = RejectedIntent;

export interface MergeDecision {
  /** True when the pulled server snapshot should be written into the store. */
  adopt: boolean;
  /** Merged display snapshot when adopt is true (server fields + live intent overlay). */
  snapshot?: JobSnapshot;
  outcome: 'server-state' | 'intent-applied' | 'conflict';
  /** Set when outcome === 'conflict'. */
  conflict?: LocalConflict;
}

/**
 * Three-way merge of one pulled job: (base / client-intent / server-state).
 *
 * Rules (build directive + ARCHITECTURE.md §6/§7 — enforced by tests in
 * tests/sync.conflict.test.ts, not aspirational):
 *
 *  - **Server-authoritative fields** (never taken from the client):
 *    `status`, `technician_id`, timestamps, prices/estimates, payment state,
 *    inspection state, vehicle history entries. The client proposes; the
 *    jobs service decides. A live intent may only overlay `status` — and
 *    only while the proposed transition is still legal from the server's
 *    current state (pure guard mirror in domain/job-state).
 *  - **No last-write-wins**: when the server moved into a state that
 *    contradicts the intent (e.g. job cancelled while the technician was
 *    offline), the result is a CONFLICT record on the server snapshot —
 *    the optimistic overlay is dropped, the contradiction is surfaced and
 *    stays visible until acknowledged, and the op's own 409 (when it lands)
 *    is re-based into a persistent rejected-intent record.
 *  - **Intent already applied** (server.status === intent.to_status): the
 *    server snapshot wins outright; the overlay is dropped.
 *  - **No mutation of append-only data**: transition rows are merged
 *    insert-only (server rows win; local pending rows are additive), so
 *    conflicts on them are impossible by construction.
 */
export function mergeIncoming(job: JobSnapshot, store: JobLocalStore): MergeDecision {
  const intent = store.pendingIntentForJob(job.id);
  if (!intent) {
    return { adopt: true, snapshot: job, outcome: 'server-state' };
  }
  const base = store.serverBaseForJob(job.id);
  const merged = threeWayMerge(base, job, intent);
  switch (merged.kind) {
    case 'server-state':
      return { adopt: true, snapshot: job, outcome: 'server-state' };
    case 'intent-applied':
      return { adopt: true, snapshot: merged.snapshot, outcome: 'intent-applied' };
    case 'conflict':
      return {
        adopt: true,
        snapshot: merged.snapshot,
        outcome: 'conflict',
        conflict: {
          op_id: intent.op_id,
          idempotency_key: intent.idempotency_key,
          proposed_status: intent.to_status,
          server_status: job.status,
          reason: merged.reason,
          at: job.updated_at,
        },
      };
  }
}
