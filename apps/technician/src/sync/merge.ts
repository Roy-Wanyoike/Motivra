import type { JobSnapshot, PendingIntent } from './api';
import { guard, isKnownStatus, type TransitionCheck } from '../domain/job-state';

/**
 * Three-way merge core: (base / client-intent / server-state).
 *
 * This module is pure — no I/O, no React, no clock. The reconciler feeds it
 * the last synced server snapshot (base), the fresh server snapshot, and the
 * live client intent (if any); it decides what the local read model shows.
 *
 * Field ownership (enforced here, tested in tests/sync.conflict.test.ts):
 *  - server owns everything except the single optimistic `status` overlay;
 *  - the overlay exists only while the intent is live AND the proposed
 *    transition is still legal from the server's current state;
 *  - a contradicting server state produces a `conflict` outcome whose
 *    snapshot is the pure server state (never the client's wish).
 */

export type ThreeWayMergeResult =
  /** No live intent, or the intent is moot (already applied). Adopt server as-is. */
  | { kind: 'server-state' }
  /** Intent overlays the server snapshot: server fields + optimistic status. */
  | { kind: 'intent-applied'; snapshot: JobSnapshot }
  /** Server state contradicts the intent. Snapshot is the PURE server state. */
  | { kind: 'conflict'; snapshot: JobSnapshot; reason: string };

export function threeWayMerge(
  base: JobSnapshot | null,
  server: JobSnapshot,
  intent: PendingIntent | null,
): ThreeWayMergeResult {
  if (!intent) return { kind: 'server-state' };

  // Defensive: a malformed intent (unknown status string) can never be
  // overlayed — treat it as a conflict against the server state.
  const proposed = intent.to_status;
  if (!isKnownStatus(proposed)) {
    return { kind: 'conflict', snapshot: server, reason: `unknown status '${intent.to_status}' in pending intent` };
  }

  // Intent already applied (or made moot) — the server snapshot wins outright
  // and the overlay is dropped. This also covers the common race where the
  // accept op landed server-side before this pull arrived.
  if (server.status === intent.to_status) {
    return { kind: 'server-state' };
  }

  // Drift detection: if the server hasn't moved since the base was synced
  // (same updated_at), the intent was created against exactly this state and
  // no re-validation is needed. If the server DID move, the intent must
  // survive the state-machine guard against the NEW server state.
  const serverMoved = !base || base.updated_at !== server.updated_at;
  if (serverMoved) {
    // An unknown server status cannot be validated against — conflict.
    if (!isKnownStatus(server.status)) {
      return { kind: 'conflict', snapshot: server, reason: `server reports unknown status '${server.status}'` };
    }
    const check: TransitionCheck = guard(proposed, server.status);
    if (!check.ok) {
      return { kind: 'conflict', snapshot: server, reason: check.reason };
    }
  }

  // Live, still-legal intent: overlay ONLY the status field. Every other
  // field (technician_id, timestamps, transitions) stays server-authoritative.
  return {
    kind: 'intent-applied',
    snapshot: { ...server, status: intent.to_status, pending_intent: intent },
  };
}
