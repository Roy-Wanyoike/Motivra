/** Sync — push → re-base → pull reconciler (issues #29, #55). */
export { SyncReconciler } from './reconciler';
export type { SyncDeps, SyncSummary } from './reconciler';
export { threeWayMerge } from './merge';
export type { ThreeWayMergeResult } from './merge';
export { mergeIncoming } from './api';
export type {
  JobLocalStore,
  JobSnapshot,
  JobTransitionRow,
  LocalConflict,
  MergeDecision,
  PendingIntent,
  PullFn,
  PullPage,
  RejectedIntent,
} from './api';
