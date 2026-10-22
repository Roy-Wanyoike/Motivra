import { TechnicianApiClient } from '../api/technician';
import type { OutboxDispatcher, OutboxOperation, DispatchResult } from '../outbox/types';
import { parseProfilePayload } from './profile';

/**
 * Outbox op type for the technician skills profile. The payload is the exact
 * POST /v1/technician/profile body (contracts/technician/openapi.yaml,
 * ProfileInput) and the op's idempotency_key is sent as the Idempotency-Key
 * header — at-most-once per key server-side, safe to replay across the
 * outbox's backoff/reclaim cycles.
 */
export const TECHNICIAN_PROFILE_OP = 'technician.profile.submit';

/**
 * Dispatcher that routes technician-pipeline outbox ops to the typed
 * contract client.
 *
 * Classification (mirrors the outbox contract in src/outbox/types.ts):
 *  - 2xx                              → accepted (op DONE)
 *  - 4xx problem (validation/403/409) → rejected (FAILED — retry cannot help)
 *  - backend pending (Phase 1)        → retryable-failure — the op STAYS
 *    QUEUED and keeps its backoff schedule, which is the honest degraded
 *    mode: the draft syncs automatically once the Phase 2 backend (#166)
 *    is configured. No simulated success, ever.
 *  - network failure / 5xx            → retryable-failure
 */
export class TechnicianOutboxDispatcher implements OutboxDispatcher {
  private readonly client: TechnicianApiClient;

  constructor(client: TechnicianApiClient) {
    this.client = client;
  }

  async dispatch(op: OutboxOperation): Promise<DispatchResult> {
    switch (op.type) {
      case TECHNICIAN_PROFILE_OP: {
        const parsed = parseProfilePayload(op.payload);
        if (!parsed.ok) {
          return { kind: 'rejected', reason: `queued profile payload no longer matches the contract: ${parsed.reason}` };
        }
        const result = await this.client.submitProfile(parsed.input, op.idempotency_key);
        switch (result.kind) {
          case 'ok':
            return { kind: 'accepted' };
          case 'problem':
            return {
              kind: 'rejected',
              reason: `server rejected the profile (${result.status} ${result.problem.code}): ${result.problem.title}`,
            };
          case 'backend-pending':
            return {
              kind: 'retryable-failure',
              reason: 'technician backend pending (Phase 2, issue #166) — op stays queued on device',
            };
          case 'unreachable':
            return { kind: 'retryable-failure', reason: result.message };
        }
        break;
      }
      default:
        return { kind: 'rejected', reason: `unknown technician pipeline op type: ${op.type}` };
    }
  }
}
