/**
 * Motivra Tech — job state machine guards (pure functions, zero runtime deps).
 *
 * Mirrors `backend/jobs/statemachine.go` and the 18-status `JobStatus` enum in
 * `contracts/jobs/openapi.yaml`. The server has the final say on every move
 * (illegal moves are rejected with 409); these guards exist so the technician
 * UI can predict server behaviour offline and so outbox ops are never enqueued
 * for a move the server would refuse.
 *
 * Guard rules (from contracts/jobs/openapi.yaml + backend/jobs/README.md):
 *   - No repair before approval: REPAIRING is reachable only from APPROVED.
 *   - No assignment outside DISPATCHING; reassignment steps back through it.
 *   - Cancellation only before physical work (CREATED..ACCEPTED, plus
 *     AWAITING_APPROVAL). After ARRIVED the outcomes are COMPLETED or FAILED.
 *   - COMPLETED, CANCELLED, FAILED are terminal and immutable.
 *   - ESCALATED is a reachable dead end (no outgoing edges, not terminal).
 */

/** The 18 job statuses, in lifecycle order (mirrors the jobs CHECK constraint). */
export const JOB_STATUSES = [
  'CREATED',
  'TRIAGING',
  'DISPATCHING',
  'ASSIGNED',
  'ACCEPTED',
  'EN_ROUTE',
  'ARRIVED',
  'INSPECTION',
  'DIAGNOSIS',
  'ESTIMATE',
  'AWAITING_APPROVAL',
  'APPROVED',
  'REPAIRING',
  'VERIFICATION',
  'COMPLETED',
  'CANCELLED',
  'FAILED',
  'ESCALATED',
] as const;

export type JobStatus = (typeof JOB_STATUSES)[number];

/** Terminal statuses: immutable once reached (no outgoing edges anywhere). */
export const TERMINAL_STATUSES: readonly JobStatus[] = [
  'COMPLETED',
  'CANCELLED',
  'FAILED',
] as const;

/**
 * Single source of truth for legal moves, copied from
 * `backend/jobs/statemachine.go` `transitions` (keep both in sync — the
 * exhaustive tests in tests/job-state.guards.test.ts pin the shape).
 * The special edges carry domain meaning:
 *  - ASSIGNED -> DISPATCHING: dispatch reassignment before acceptance.
 *  - ACCEPTED -> DISPATCHING: technician released the job before going en route.
 *  - REPAIRING -> VERIFICATION is progress; VERIFICATION -> REPAIRING is rework.
 */
export const VALID_TRANSITIONS: Readonly<
  Record<JobStatus, readonly JobStatus[]>
> = Object.freeze({
  CREATED: ['TRIAGING', 'CANCELLED'],
  TRIAGING: ['DISPATCHING', 'CANCELLED'],
  DISPATCHING: ['ASSIGNED', 'ESCALATED', 'CANCELLED'],
  ASSIGNED: ['ACCEPTED', 'DISPATCHING', 'CANCELLED'],
  ACCEPTED: ['EN_ROUTE', 'DISPATCHING', 'CANCELLED'],
  EN_ROUTE: ['ARRIVED', 'CANCELLED'],
  ARRIVED: ['INSPECTION', 'FAILED'],
  INSPECTION: ['DIAGNOSIS'],
  DIAGNOSIS: ['ESTIMATE'],
  ESTIMATE: ['AWAITING_APPROVAL'],
  AWAITING_APPROVAL: ['APPROVED', 'ESCALATED', 'CANCELLED'],
  APPROVED: ['REPAIRING'],
  REPAIRING: ['VERIFICATION', 'FAILED'],
  VERIFICATION: ['COMPLETED', 'REPAIRING'],
  // Terminal statuses and the ESCALATED dead end have no outgoing edges.
  COMPLETED: [],
  CANCELLED: [],
  FAILED: [],
  ESCALATED: [],
});

const STATUS_SET: ReadonlySet<string> = new Set(JOB_STATUSES);

export function isKnownStatus(value: string): value is JobStatus {
  return STATUS_SET.has(value);
}

export function isTerminal(status: JobStatus): boolean {
  return TERMINAL_STATUSES.includes(status);
}

export type TransitionCheck =
  | { ok: true }
  | { ok: false; reason: string };

/** Actionable error message listing the legal alternatives, mirroring Go's CanTransition. */
export class IllegalTransitionError extends Error {
  readonly from: JobStatus | 'UNKNOWN';
  readonly to: JobStatus;

  constructor(from: JobStatus | 'UNKNOWN', to: JobStatus, reason: string) {
    super(reason);
    this.name = 'IllegalTransitionError';
    this.from = from;
    this.to = to;
  }
}

/** Sorted legal target statuses from `current` (empty for terminal/dead-end/unknown). */
export function legalTargets(current: JobStatus): readonly JobStatus[] {
  return [...VALID_TRANSITIONS[current]].sort();
}

/**
 * Pure transition guard. Returns `{ ok: true }` when the server would accept
 * the move (2xx), `{ ok: false, reason }` when it would reject with 409.
 * Never throws — UI decides how to surface the reason.
 */
export function guard(next: JobStatus, current: JobStatus): TransitionCheck {
  const targets = VALID_TRANSITIONS[current];
  if (targets.includes(next)) return { ok: true };
  if (targets.length === 0) {
    return {
      ok: false,
      reason: isTerminal(current)
        ? `illegal transition ${current} -> ${next}: ${current} is terminal and immutable`
        : `illegal transition ${current} -> ${next}: ${current} has no outgoing transitions (dead end)`,
    };
  }
  return {
    ok: false,
    reason: `illegal transition ${current} -> ${next}; legal transitions from ${current} are [${legalTargets(current).join(' ')}]`,
  };
}

/**
 * Guard that throws `IllegalTransitionError` on failure — for call sites that
 * treat an illegal move as a programming error (the outbox enqueue path uses
 * the non-throwing `guard` and surfaces the reason in the UI).
 */
export function guardOrThrow(next: JobStatus, current: JobStatus): void {
  const check = guard(next, current);
  if (!check.ok) throw new IllegalTransitionError(current, next, check.reason);
}

/**
 * Human-readable label + ordering index for the JobDetail timeline. Derived
 * from JOB_STATUSES order; ESCALATED intentionally sorts last (dead end).
 */
export function statusRank(status: JobStatus): number {
  return JOB_STATUSES.indexOf(status);
}
