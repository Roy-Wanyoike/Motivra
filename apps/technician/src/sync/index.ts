/** Sync — push/pull reconciler skeleton (issue #29). */
export { SyncReconciler } from './reconciler';
export type { SyncDeps, SyncSummary } from './reconciler';
export { shouldApplySnapshot } from './api';
export type {
  JobLocalStore,
  JobSnapshot,
  JobTransitionRow,
  PullFn,
  PullPage,
} from './api';
