import { createContext, useCallback, useContext, useEffect, useMemo, useState } from 'react';
import type { ReactNode } from 'react';

import {
  TechnicianApiClient,
  technicianApiConfigFromEnv,
  type EarningsSummary,
  type VerificationState,
} from '../api/technician';
import { Outbox } from '../outbox/outbox';
import { InMemoryOutboxStorage } from '../outbox/storage';
import { uuidv4 } from '../lib/uuid';
import { TECHNICIAN_PROFILE_OP, TechnicianOutboxDispatcher } from './dispatcher';
import {
  EMPTY_PROFILE_DRAFT,
  profileInputFromDraft,
  profilePayloadFromInput,
  validateProfileDraft,
  type ProfileDraft,
  type ValidationIssue,
} from './profile';

/**
 * React wiring for the technician pipeline (issue #126 Phase 1).
 *
 * Owns the pipeline's OWN outbox (separate from the jobs sync outbox):
 * skills-profile submissions go in as `technician.profile.submit` ops with
 * the contract-shaped payload and are replayed with backoff until the
 * Phase 2 backend (issue #166) exists to accept them. Reads (verification
 * status, earnings) go through the typed contract client and report
 * honestly in degraded mode — never simulated data (repo principle 10).
 *
 * Persistence honesty: the pipeline outbox uses the in-memory storage
 * adapter like the jobs outbox (SQLite adapter is the documented follow-up
 * in apps/technician/README.md) — drafts survive within a session, not
 * across app restarts, and the UI says so.
 */

export interface QueuedProfileOp {
  op_id: string;
  idempotency_key: string;
  enqueued_at: string;
  state: 'PENDING' | 'IN_FLIGHT' | 'DONE' | 'FAILED';
  attempts: number;
  last_error: string | null;
}

/**
 * Where the displayed verification state came from. Phase 1 has no backend,
 * so the honest source is 'device-default' (a brand-new technician is
 * unverified until a verifier says otherwise); 'server' only appears once
 * GET /v1/technician/verification-status actually answers.
 */
export interface VerificationView {
  source: 'device-default' | 'server';
  state: VerificationState;
  /** Honest note shown under the badge (why this state, what's pending). */
  note: string;
}

export type EarningsView =
  | { kind: 'backend-pending'; note: string }
  | { kind: 'ok'; summary: EarningsSummary }
  | { kind: 'error'; message: string };

export interface SubmitResult {
  ok: boolean;
  issues?: ValidationIssue[];
}

export interface PipelineApi {
  draft: ProfileDraft;
  setDraft: (patch: Partial<ProfileDraft>) => void;
  /** Live validation of the current draft (contract-mirrored rules). */
  draftIssues: ValidationIssue[];
  /** Validate → enqueue the profile op (idempotent per submission). */
  submitDraft: () => Promise<SubmitResult>;
  queuedOp: QueuedProfileOp | null;
  verification: VerificationView;
  earnings: EarningsView;
  /** True when no backend is configured (Phase-1 degraded mode). */
  backendDegraded: boolean;
  /** Drain the pipeline outbox + re-read the contract endpoints. */
  refresh: () => Promise<void>;
}

const PipelineContext = createContext<PipelineApi | null>(null);

export function TechnicianPipelineProvider({ children }: { children: ReactNode }) {
  const [{ client, outbox }] = useState<{ client: TechnicianApiClient; outbox: Outbox }>(() => {
    const api = new TechnicianApiClient(technicianApiConfigFromEnv());
    const box = new Outbox({ storage: new InMemoryOutboxStorage(), dispatcher: new TechnicianOutboxDispatcher(api) });
    return { client: api, outbox: box };
  });

  const [draft, setDraftState] = useState<ProfileDraft>(EMPTY_PROFILE_DRAFT);
  const [queuedOpId, setQueuedOpId] = useState<string | null>(null);
  const [queuedOp, setQueuedOp] = useState<QueuedProfileOp | null>(null);
  const [verification, setVerification] = useState<VerificationView>(() =>
    client.degraded
      ? {
          source: 'device-default',
          state: 'unverified',
          note: 'Verification backend is Phase 2 (issue #166). Until it is connected, this device shows the honest default for a new technician: unverified.',
        }
      : { source: 'device-default', state: 'unverified', note: 'Checking with the server…' },
  );
  const [earnings, setEarnings] = useState<EarningsView>(() =>
    client.degraded
      ? { kind: 'backend-pending', note: '' }
      : { kind: 'error', message: 'Not loaded yet — pull to refresh.' },
  );

  const setDraft = useCallback((patch: Partial<ProfileDraft>) => {
    setDraftState((prev) => ({ ...prev, ...patch }));
  }, []);

  const readQueuedOp = useCallback(
    async (opId: string): Promise<QueuedProfileOp | null> => {
      const op = await outbox.storage.get(opId);
      if (!op) return null;
      return {
        op_id: op.id,
        idempotency_key: op.idempotency_key,
        enqueued_at: op.created_at,
        state: op.state,
        attempts: op.attempts,
        last_error: op.last_error ?? null,
      };
    },
    [outbox],
  );

  const refresh = useCallback(async () => {
    // 1. Drain the pipeline outbox (profile ops retry with backoff until the
    //    Phase 2 backend exists; a queued op is the durable draft).
    await outbox.drain();

    // 2. Reflect the queued op's latest state.
    if (queuedOpId !== null) {
      setQueuedOp(await readQueuedOp(queuedOpId));
    }

    // 3. Verification status — server read when possible, honest default otherwise.
    if (client.degraded) {
      setVerification({
        source: 'device-default',
        state: 'unverified',
        note: 'Verification backend is Phase 2 (issue #166). Until it is connected, this device shows the honest default for a new technician: unverified.',
      });
    } else {
      const result = await client.getVerificationStatus();
      if (result.kind === 'ok') {
        setVerification({ source: 'server', state: result.data.status, note: `Server state as of ${result.data.updated_at}.` });
      } else {
        const detail =
          result.kind === 'problem'
            ? `${result.status} ${result.problem.code}: ${result.problem.title}`
            : result.kind === 'backend-pending'
              ? 'backend pending (Phase 2, issue #166)'
              : result.message;
        setVerification({ source: 'device-default', state: 'unverified', note: `Could not read server status (${detail}); showing the device default.` });
      }
    }

    // 4. Earnings — Phase 4 endpoint (#168); degraded mode reports it plainly.
    if (client.degraded) {
      setEarnings({ kind: 'backend-pending', note: '' });
    } else {
      const result = await client.getEarnings();
      if (result.kind === 'ok') {
        setEarnings({ kind: 'ok', summary: result.data });
      } else if (result.kind === 'backend-pending') {
        setEarnings({ kind: 'backend-pending', note: '' });
      } else {
        setEarnings({
          kind: 'error',
          message:
            result.kind === 'problem'
              ? `${result.status} ${result.problem.code}: ${result.problem.title}`
              : result.message,
        });
      }
    }
  }, [client, outbox, queuedOpId, readQueuedOp]);

  const submitDraft = useCallback(
    async (): Promise<SubmitResult> => {
      const issues = validateProfileDraft(draft);
      if (issues.length > 0) {
        return { ok: false, issues };
      }
      // Reuse the live op's key when one is queued (the outbox dedupes);
      // fresh key after a FAILED op so a corrected profile is a new op.
      const reuseKey = queuedOp !== null && queuedOp.state !== 'FAILED' ? queuedOp.idempotency_key : undefined;
      const { operation } = await outbox.enqueue({
        type: TECHNICIAN_PROFILE_OP,
        payload: profilePayloadFromInput(profileInputFromDraft(draft)),
        idempotencyKey: reuseKey ?? uuidv4(),
      });
      setQueuedOpId(operation.id);
      setQueuedOp({
        op_id: operation.id,
        idempotency_key: operation.idempotency_key,
        enqueued_at: operation.created_at,
        state: operation.state,
        attempts: operation.attempts,
        last_error: operation.last_error ?? null,
      });
      await refresh();
      return { ok: true };
    },
    [draft, queuedOp, outbox, refresh],
  );

  // One opportunistic refresh on mount (mirrors SyncProvider's behaviour).
  useEffect(() => {
    void refresh();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const draftIssues = useMemo(() => validateProfileDraft(draft), [draft]);

  const value = useMemo<PipelineApi>(
    () => ({
      draft,
      setDraft,
      draftIssues,
      submitDraft,
      queuedOp,
      verification,
      earnings,
      backendDegraded: client.degraded,
      refresh,
    }),
    [draft, setDraft, draftIssues, submitDraft, queuedOp, verification, earnings, client, refresh],
  );

  return <PipelineContext.Provider value={value}>{children}</PipelineContext.Provider>;
}

export function usePipeline(): PipelineApi {
  const ctx = useContext(PipelineContext);
  if (!ctx) throw new Error('usePipeline must be used inside <TechnicianPipelineProvider>');
  return ctx;
}
