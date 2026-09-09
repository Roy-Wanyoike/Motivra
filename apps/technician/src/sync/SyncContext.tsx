import { createContext, useCallback, useContext, useEffect, useMemo, useState } from 'react';
import type { ReactNode } from 'react';

import { Outbox } from '../outbox/outbox';
import { InMemoryOutboxStorage } from '../outbox/storage';
import type { OutboxDispatcher } from '../outbox/types';
import { SyncReconciler } from './reconciler';
import type { JobLocalStore, PullFn } from './api';
import type { SyncSummary } from './reconciler';

/**
 * React wiring for the outbox + reconciler. Holds one Outbox/Reconciler per
 * provider, exposes coarse status for the global sync banner and a manual
 * `syncNow()` (auto-interval and connectivity-triggered sync are follow-ups
 * once @react-native-community/netinfo is allowed into the lean dep set).
 */

export type SyncStatus = 'idle' | 'syncing' | 'ok' | 'error';

export interface SyncApi {
  outbox: Outbox;
  status: SyncStatus;
  lastSummary: SyncSummary | null;
  lastError: string | null;
  /** Pending (PENDING + IN_FLIGHT) outbox ops — drives the offline badge. */
  pendingCount: number;
  syncNow: () => Promise<void>;
}

const SyncContext = createContext<SyncApi | null>(null);

export interface SyncProviderProps {
  dispatcher: OutboxDispatcher;
  pull: PullFn;
  store: JobLocalStore;
  children: ReactNode;
}

export function SyncProvider({ dispatcher, pull, store, children }: SyncProviderProps) {
  // Created once per provider lifetime (lazy useState initializer).
  const [{ outbox, reconciler }] = useState<{ outbox: Outbox; reconciler: SyncReconciler }>(() => {
    const box = new Outbox({ storage: new InMemoryOutboxStorage(), dispatcher });
    return { outbox: box, reconciler: new SyncReconciler({ outbox: box, pull, store }) };
  });

  const [status, setStatus] = useState<SyncStatus>('idle');
  const [lastSummary, setLastSummary] = useState<SyncSummary | null>(null);
  const [lastError, setLastError] = useState<string | null>(null);
  const [pendingCount, setPendingCount] = useState(0);

  const refreshPending = useCallback(async () => {
    setPendingCount(await outbox.pendingCount());
  }, [outbox]);

  const syncNow = useCallback(async () => {
    setStatus('syncing');
    setLastError(null);
    try {
      const summary = await reconciler.sync(null);
      setLastSummary(summary);
      setStatus('ok');
    } catch (err) {
      setLastError(err instanceof Error ? err.message : String(err));
      setStatus('error');
    } finally {
      await refreshPending();
    }
  }, [reconciler, refreshPending]);

  // One opportunistic sync on mount (stub pull is instant; a real transport
  // would no-op fast when offline). Interval polling is a follow-up.
  useEffect(() => {
    void syncNow();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const value = useMemo<SyncApi>(
    () => ({ outbox, status, lastSummary, lastError, pendingCount, syncNow }),
    [outbox, status, lastSummary, lastError, pendingCount, syncNow],
  );

  return <SyncContext.Provider value={value}>{children}</SyncContext.Provider>;
}

export function useSync(): SyncApi {
  const ctx = useContext(SyncContext);
  if (!ctx) throw new Error('useSync must be used inside <SyncProvider>');
  return ctx;
}
