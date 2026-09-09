import type { JobSnapshot } from '../sync/api';
import type { DispatchResult, OutboxDispatcher, OutboxOperation } from '../outbox/types';

/**
 * STUB DATA + STUB API — nothing here talks to a real server. The real
 * transport (fetch against `contracts/jobs` paths, JWT from the identity
 * service) is a documented follow-up; see the README honesty table.
 */

const now = '2026-09-09T08:00:00Z';

export const MOCK_JOBS: JobSnapshot[] = [
  {
    id: '11111111-1111-4111-8111-111111111111',
    status: 'ASSIGNED',
    problem_summary: 'Won\'t start — starter clicking, Nairobi CBD',
    technician_id: 'aaaaaaaa-0000-4000-8000-aaaaaaaaaaaa',
    vehicle_id: 'bbbbbbbb-0000-4000-8000-bbbbbbbbbbbb',
    created_at: now,
    updated_at: now,
    transitions: [
      { id: 1, job_id: '11111111-1111-4111-8111-111111111111', from_status: 'CREATED', to_status: 'TRIAGING', actor_id: null, reason: 'intake', created_at: now },
      { id: 2, job_id: '11111111-1111-4111-8111-111111111111', from_status: 'TRIAGING', to_status: 'DISPATCHING', actor_id: null, reason: 'routed to mobile crew', created_at: now },
      { id: 3, job_id: '11111111-1111-4111-8111-111111111111', from_status: 'DISPATCHING', to_status: 'ASSIGNED', actor_id: 'aaaaaaaa-0000-4000-8000-aaaaaaaaaaaa', reason: 'assigned to J. Kamau', created_at: now },
    ],
  },
  {
    id: '22222222-2222-4222-8222-222222222222',
    status: 'DIAGNOSIS',
    problem_summary: 'Brake pads worn — Westlands pickup',
    technician_id: 'aaaaaaaa-0000-4000-8000-aaaaaaaaaaaa',
    vehicle_id: 'cccccccc-0000-4000-8000-cccccccccccc',
    created_at: now,
    updated_at: now,
    transitions: [
      { id: 1, job_id: '22222222-2222-4222-8222-222222222222', from_status: 'CREATED', to_status: 'TRIAGING', actor_id: null, reason: 'intake', created_at: now },
      { id: 2, job_id: '22222222-2222-4222-8222-222222222222', from_status: 'TRIAGING', to_status: 'DISPATCHING', actor_id: null, reason: 'routed', created_at: now },
      { id: 3, job_id: '22222222-2222-4222-8222-222222222222', from_status: 'DISPATCHING', to_status: 'ASSIGNED', actor_id: null, reason: 'assigned', created_at: now },
      { id: 4, job_id: '22222222-2222-4222-8222-222222222222', from_status: 'ASSIGNED', to_status: 'ACCEPTED', actor_id: 'aaaaaaaa-0000-4000-8000-aaaaaaaaaaaa', reason: 'accepted on device', created_at: now },
      { id: 5, job_id: '22222222-2222-4222-8222-222222222222', from_status: 'ACCEPTED', to_status: 'EN_ROUTE', actor_id: 'aaaaaaaa-0000-4000-8000-aaaaaaaaaaaa', reason: 'departed', created_at: now },
      { id: 6, job_id: '22222222-2222-4222-8222-222222222222', from_status: 'EN_ROUTE', to_status: 'ARRIVED', actor_id: 'aaaaaaaa-0000-4000-8000-aaaaaaaaaaaa', reason: 'on site', created_at: now },
      { id: 7, job_id: '22222222-2222-4222-8222-222222222222', from_status: 'ARRIVED', to_status: 'INSPECTION', actor_id: 'aaaaaaaa-0000-4000-8000-aaaaaaaaaaaa', reason: null, created_at: now },
      { id: 8, job_id: '22222222-2222-4222-8222-222222222222', from_status: 'INSPECTION', to_status: 'DIAGNOSIS', actor_id: 'aaaaaaaa-0000-4000-8000-aaaaaaaaaaaa', reason: 'pads at 2mm', created_at: now },
    ],
  },
  {
    id: '33333333-3333-4333-8333-333333333333',
    status: 'REPAIRING',
    problem_summary: 'Alternator replacement — Kilimani',
    technician_id: 'aaaaaaaa-0000-4000-8000-aaaaaaaaaaaa',
    vehicle_id: 'dddddddd-0000-4000-8000-dddddddddddd',
    created_at: now,
    updated_at: now,
    transitions: [
      { id: 1, job_id: '33333333-3333-4333-8333-333333333333', from_status: 'CREATED', to_status: 'TRIAGING', actor_id: null, reason: 'intake', created_at: now },
      { id: 2, job_id: '33333333-3333-4333-8333-333333333333', from_status: 'AWAITING_APPROVAL', to_status: 'APPROVED', actor_id: null, reason: 'customer approved via USSD', created_at: now },
      { id: 3, job_id: '33333333-3333-4333-8333-333333333333', from_status: 'APPROVED', to_status: 'REPAIRING', actor_id: 'aaaaaaaa-0000-4000-8000-aaaaaaaaaaaa', reason: null, created_at: now },
    ],
  },
];

/**
 * Simulated API used as the app's dispatcher until the real transport lands.
 * Behaviour is deliberately simple and observable:
 *  - accepts every op with a unique idempotency key (server at-most-once per key),
 *  - records applied keys so tests/demo can prove no double-apply,
 *  - can be told to fail retryably N times to demo backoff.
 */
export class SimulatedJobsApi implements OutboxDispatcher {
  /** idempotency_key -> times applied (must stay 1 per key in honest demos). */
  readonly appliedKeys = new Map<string, number>();
  private retryableFailuresRemaining: number;

  constructor(retryableFailuresBeforeSuccess = 0) {
    this.retryableFailuresRemaining = retryableFailuresBeforeSuccess;
  }

  async dispatch(op: OutboxOperation): Promise<DispatchResult> {
    if (this.retryableFailuresRemaining > 0) {
      this.retryableFailuresRemaining -= 1;
      return { kind: 'retryable-failure', reason: 'simulated network failure (offline)' };
    }
    const seen = this.appliedKeys.get(op.idempotency_key) ?? 0;
    this.appliedKeys.set(op.idempotency_key, seen + 1);
    // A real server would apply once per key and return 200 regardless of replay.
    return { kind: 'accepted' };
  }
}
