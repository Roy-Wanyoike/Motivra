/**
 * Outbox — the offline-first mutation queue (issue #29 core deliverable).
 *
 * Every mutation the technician makes offline (job transition, evidence
 * upload intent, …) becomes an `OutboxOperation` with an idempotency key and
 * is delivered by `Outbox.drain()` with exponential backoff + jitter.
 */
export { Outbox } from './outbox';
export { InMemoryOutboxStorage } from './storage';
export type { OutboxStorageAdapter } from './storage';
export { computeBackoffDelayMs, DEFAULT_BACKOFF_POLICY } from './backoff';
export type { BackoffPolicy } from './backoff';
export type {
  DispatchResult,
  DrainReport,
  EnqueueInput,
  EnqueueResult,
  OutboxDispatcher,
  OutboxOperation,
  OutboxOperationState,
} from './types';
