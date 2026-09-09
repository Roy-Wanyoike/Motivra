import type { JsonValue } from '../lib/json';

/**
 * Outbox operation lifecycle:
 *
 *   PENDING ──drain──▶ IN_FLIGHT ──accepted──▶ DONE
 *      ▲                   │          └─permanent 4xx─▶ FAILED (poison, not retried)
 *      │                   └─retryable─▶ PENDING (attempts++, next_attempt_at = backoff)
 *      └─lease reclaim (crash between IN_FLIGHT and the state write)
 *
 *   FAILED is terminal locally; the server's idempotency layer is what makes
 *   replay of any interrupted op safe (see Outbox idempotency contract below).
 */
export type OutboxOperationState = 'PENDING' | 'IN_FLIGHT' | 'FAILED' | 'DONE';

/**
 * One queued mutation. Field names match the issue #29 schema verbatim
 * (snake_case) so a SQLite mirror table (expo-sqlite follow-up) can use the
 * same column names.
 */
export interface OutboxOperation {
  /** UUIDv4 operation id. */
  id: string;
  /** Operation type, e.g. `job.transition`. */
  type: string;
  /** JSON payload — the request body the dispatcher will send. */
  payload: JsonValue;
  /**
   * Client-generated idempotency key sent as `Idempotency-Key`. The server
   * must apply at-most-once per key; the client may safely replay the same
   * operation any number of times (crash, lease reclaim, user retry).
   */
  idempotency_key: string;
  /** ISO-8601 creation timestamp. */
  created_at: string;
  /** Number of dispatch attempts made so far (0 on first enqueue). */
  attempts: number;
  /**
   * ISO-8601 earliest time the op may be picked up again (backoff schedule).
   * While state=IN_FLIGHT the field doubles as the lease-start timestamp so
   * stale leases can be reclaimed after a crash without extra schema columns.
   */
  next_attempt_at: string;
  state: OutboxOperationState;
  /** Last dispatcher error message, for the UI and support triage. */
  last_error?: string | null;
  /** Monotonic sequence for stable ordering (set by the storage adapter). */
  seq: number;
}

export interface EnqueueInput {
  type: string;
  payload: JsonValue;
  /** Omit to auto-generate a UUIDv4 key. */
  idempotencyKey?: string;
}

export interface EnqueueResult {
  operation: OutboxOperation;
  /** True when an operation with the same idempotency key already existed and was reused. */
  deduped: boolean;
}

/** Result of one dispatch attempt against the (stub or real) API. */
export type DispatchResult =
  /** 2xx — server accepted and durably applied. */
  | { kind: 'accepted' }
  /** 4xx-class permanent rejection (validation, 409 conflict). Retry will not help. */
  | { kind: 'rejected'; reason: string }
  /** Network failure / 5xx / timeout — safe to retry with backoff. */
  | { kind: 'retryable-failure'; reason: string };

/**
 * Transport abstraction. Implementations MUST forward `op.idempotency_key`
 * (HTTP header `Idempotency-Key`) and MUST treat a "dispatch succeeded on the
 * server but the client crashed before recording DONE" replay as safe.
 */
export interface OutboxDispatcher {
  dispatch(op: OutboxOperation): Promise<DispatchResult>;
}

/** Reason a drained op ended outside DONE. */
export interface DrainReport {
  /** Ops picked up this run. */
  processed: number;
  succeeded: number;
  /** Permanent 4xx failures — parked as FAILED, surfaced to the user. */
  failedPermanent: number;
  /** Retryable failures — re-armed with exponential backoff + jitter. */
  failedRetryable: number;
  /** Ops skipped because another drain run held the IN_FLIGHT lease. */
  skippedInFlight: number;
}
