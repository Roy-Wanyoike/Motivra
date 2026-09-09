import { useState } from 'react';
import { Pressable, ScrollView, StyleSheet, Text, View } from 'react-native';

import { guard, legalTargets } from '../domain/job-state';
import type { JobStatus } from '../domain/job-state';
import { isKnownStatus } from '../domain/job-state';
import { useNavigation } from '../navigation/NavigationContext';
import { useSync } from '../sync/SyncContext';
import { JobStore } from '../state/job-store';

/**
 * Job detail screen: append-only status timeline + transition actions.
 *
 * Action flow (the offline-first contract, in order):
 *   1. `guard(next, current)` — pure client-side mirror of the server state
 *      machine; illegal moves never leave the device.
 *   2. enqueue `job.transition` into the outbox with a fresh idempotency key
 *      (payload mirrors POST /v1/jobs/{jobID}/transitions: { to, reason }).
 *   3. optimistic local status update; the server corrects us on next pull
 *      if it rejects (its 409 is authoritative — no last-write-wins).
 */
export function JobDetailScreen({ jobId, jobStore }: { jobId: string; jobStore: JobStore }) {
  const job = jobStore.get(jobId);
  const { back, canGoBack } = useNavigation();
  const { outbox, syncNow } = useSync();
  const [error, setError] = useState<string | null>(null);

  if (!job) {
    return (
      <View style={styles.container}>
        <Text>Job not found (not synced yet).</Text>
        {canGoBack ? <Pressable onPress={back}><Text style={styles.link}>← Back</Text></Pressable> : null}
      </View>
    );
  }

  const current: JobStatus = isKnownStatus(job.status) ? job.status : 'CREATED';
  const targets = legalTargets(current);

  const proposeTransition = (to: JobStatus) => {
    const check = guard(to, current);
    if (!check.ok) {
      setError(check.reason); // guard-first: illegal move never reaches the outbox
      return;
    }
    setError(null);
    void (async () => {
      const { operation } = await outbox.enqueue({
        type: 'job.transition',
        payload: { to, reason: `proposed on device from ${current}` },
      });
      jobStore.markPending(job.id, true);
      jobStore.appendPendingTransition(job.id, {
        id: -Date.now(), // negative ids mark locally-proposed, unsynced rows
        job_id: job.id,
        from_status: current,
        to_status: to,
        actor_id: null,
        reason: `pending (op ${operation.id.slice(0, 8)}…, key ${operation.idempotency_key.slice(0, 8)}…)`,
        created_at: new Date().toISOString(),
      });
      jobStore.optimisticStatus(job.id, to);
      jobStore.markPending(job.id, false);
      await syncNow();
    })();
  };

  return (
    <ScrollView style={styles.container}>
      {canGoBack ? (
        <Pressable onPress={back}>
          <Text style={styles.link}>← All jobs</Text>
        </Pressable>
      ) : null}
      <Text style={styles.title}>{job.problem_summary}</Text>
      <Text style={styles.meta}>
        {job.id} · vehicle {job.vehicle_id ?? '—'}
      </Text>
      <View style={styles.statusHeader}>
        <Text style={styles.statusLabel}>Status</Text>
        <View style={styles.statusPill}>
          <Text style={styles.statusText}>{job.status}</Text>
        </View>
      </View>

      <Text style={styles.section}>Timeline (append-only)</Text>
      <View style={styles.timeline}>
        {(job.transitions ?? []).map((t) => (
          <View key={`${t.id}-${t.created_at}`} style={styles.timelineRow}>
            <Text style={styles.timelineText}>
              {t.from_status} → {t.to_status}
              {t.id < 0 ? '  (pending sync)' : ''}
            </Text>
            <Text style={styles.timelineMeta}>
              {t.reason ?? '—'} · {t.created_at}
            </Text>
          </View>
        ))}
        {(job.transitions ?? []).length === 0 ? <Text style={styles.meta}>No transitions recorded.</Text> : null}
      </View>

      <Text style={styles.section}>Actions (guard → outbox)</Text>
      <View style={styles.actions}>
        {targets.length === 0 ? (
          <Text style={styles.meta}>
            No legal transitions — {isKnownStatus(job.status) ? 'this status is a dead end / terminal.' : 'unknown status.'}
          </Text>
        ) : (
          targets.map((to) => (
            <Pressable key={to} testID={`action-${to}`} style={styles.button} onPress={() => proposeTransition(to)}>
              <Text style={styles.buttonText}>→ {to}</Text>
            </Pressable>
          ))
        )}
      </View>

      {error ? <Text style={styles.error} testID="guard-error">{error}</Text> : null}
    </ScrollView>
  );
}

const styles = StyleSheet.create({
  container: { flex: 1, padding: 12 },
  link: { color: '#1f6feb', marginBottom: 8, fontSize: 14 },
  title: { fontSize: 20, fontWeight: '700' },
  meta: { color: '#666', fontSize: 12, marginTop: 2 },
  statusHeader: { flexDirection: 'row', alignItems: 'center', marginTop: 12 },
  statusLabel: { fontWeight: '600', marginRight: 8 },
  statusPill: { backgroundColor: '#0a3069', borderRadius: 999, paddingHorizontal: 12, paddingVertical: 4 },
  statusText: { color: '#fff', fontSize: 12, fontWeight: '700' },
  section: { fontWeight: '700', marginTop: 16, marginBottom: 6 },
  timeline: { borderWidth: StyleSheet.hairlineWidth, borderColor: '#ddd', borderRadius: 8, padding: 8 },
  timelineRow: { paddingVertical: 4, borderBottomWidth: StyleSheet.hairlineWidth, borderColor: '#eee' },
  timelineText: { fontSize: 13, fontWeight: '600' },
  timelineMeta: { fontSize: 11, color: '#777' },
  actions: { flexDirection: 'row', flexWrap: 'wrap', gap: 8 },
  button: { backgroundColor: '#1a7f37', borderRadius: 8, paddingHorizontal: 14, paddingVertical: 10 },
  buttonText: { color: '#fff', fontWeight: '700', fontSize: 13 },
  error: { color: '#c62828', marginTop: 12, fontSize: 13 },
});
