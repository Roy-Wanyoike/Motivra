import { Pressable, ScrollView, StyleSheet, Text, View } from 'react-native';

import { TRADE_LABELS, type VerificationState } from '../api/technician';
import { useNavigation } from '../navigation/NavigationContext';
import { usePipeline } from '../pipeline/TechnicianPipelineContext';
import { parseToolsText } from '../pipeline/profile';

/**
 * Profile screen (issue #126 Phase 1): verification status + skills profile
 * + the queued profile op's sync state.
 *
 * Verification status honesty contract:
 *  - the badge states its SOURCE. `device-default` means no backend has
 *    spoken yet (Phase 1) and the honest default for a new technician is
 *    shown: unverified. `server` means GET /v1/technician/verification-status
 *    actually answered (Phase 2, issue #166).
 *  - the screen explains in plain language what the credential means (and
 *    what it does NOT claim) so graduates and customers share one
 *    expectation — see docs/product/technician-pipeline.md §4.
 */

const STATUS_LABELS: Record<VerificationState, string> = {
  unverified: 'Not verified',
  assessment_pending: 'Assessment pending',
  verified: 'Verified Technician',
};

const STATUS_COLORS: Record<VerificationState, { bg: string; fg: string }> = {
  unverified: { bg: '#f1f5f9', fg: '#475569' },
  assessment_pending: { bg: '#fdf6e3', fg: '#8a5a00' },
  verified: { bg: '#e6f4ea', fg: '#1a7f37' },
};

const SOURCE_LABELS = {
  'device-default': 'shown from this device (no server yet)',
  server: 'read from the Motivra identity service',
} as const;

export function ProfileScreen() {
  const { navigate } = useNavigation();
  const { verification, queuedOp, draft, backendDegraded, refresh } = usePipeline();
  const statusStyle = STATUS_COLORS[verification.state];

  const tools = parseToolsText(draft.toolsText);

  return (
    <ScrollView style={styles.container} testID="profile-screen">
      <Text style={styles.title}>Your technician profile</Text>

      <View style={styles.statusCard}>
        <Text style={styles.sectionLabel}>Verification status</Text>
        <View style={[styles.statusBadge, { backgroundColor: statusStyle.bg }]} testID="verification-badge">
          <Text style={[styles.statusText, { color: statusStyle.fg }]}>{STATUS_LABELS[verification.state]}</Text>
        </View>
        <Text style={styles.sourceNote}>{SOURCE_LABELS[verification.source]}</Text>
        <Text style={styles.note}>{verification.note}</Text>

        <Text style={styles.heading}>What “Verified Technician” means</Text>
        <Text style={styles.body}>
          A Motivra verifier assessed you against the published skills rubric
          (an inspection template) and recorded a pass — with who verified,
          when, and against which assessment, kept tamper-evidently.
        </Text>
        <Text style={styles.body}>
          It does not claim years of experience or warranty a specific repair.
          It IS what makes you eligible for dispatched jobs: unverified
          technicians receive no dispatched work (enforced server-side).
        </Text>

        {backendDegraded ? (
          <View style={styles.pendingBox} testID="verification-pending">
            <Text style={styles.pendingText}>
              Verification reads are backend Phase 2 (issue #166). Your
              assessment outcome will appear here once a verifier records it.
            </Text>
          </View>
        ) : verification.source === 'server' && verification.state === 'verified' ? null : null}
      </View>

      <View style={styles.card}>
        <Text style={styles.sectionLabel}>Skills profile</Text>
        {queuedOp === null ? (
          <Text style={styles.body}>No profile saved yet — start the onboarding wizard.</Text>
        ) : (
          <>
            <Text style={styles.body}>
              Trade: {draft.trade ? TRADE_LABELS[draft.trade] : '—'}
            </Text>
            <Text style={styles.body}>
              Specializations: {draft.specializations.map((t) => TRADE_LABELS[t]).join(', ') || '—'}
            </Text>
            <Text style={styles.body}>Tools: {tools.length > 0 ? tools.join(', ') : 'none listed'}</Text>
            <Text style={styles.body}>
              Service area: {draft.areaName.trim() || '—'}, {draft.county.trim() || '—'}
            </Text>
            <View style={styles.syncRow} testID="profile-op-state">
              <Text style={styles.syncLabel}>
                {queuedOp.state === 'DONE'
                  ? 'Synced with the technician backend ✓'
                  : queuedOp.state === 'FAILED'
                    ? `Not delivered — ${queuedOp.last_error ?? 'rejected by server'}`
                    : `Queued on device · ${queuedOp.attempts} attempt${queuedOp.attempts === 1 ? '' : 's'}`}
              </Text>
              <Text style={styles.syncMeta}>
                op {queuedOp.op_id.slice(0, 8)}… · key {queuedOp.idempotency_key.slice(0, 8)}… · enqueued {queuedOp.enqueued_at}
              </Text>
            </View>
          </>
        )}
        <Pressable testID="edit-profile" style={styles.button} onPress={() => navigate({ name: 'Onboarding' })}>
          <Text style={styles.buttonText}>{queuedOp === null ? 'Start onboarding' : 'Edit skills profile'}</Text>
        </Pressable>
      </View>

      <Pressable testID="refresh-status" style={styles.refreshButton} onPress={() => void refresh()}>
        <Text style={styles.refreshText}>Re-check verification status</Text>
      </Pressable>
    </ScrollView>
  );
}

const styles = StyleSheet.create({
  container: { flex: 1, padding: 12 },
  title: { fontSize: 22, fontWeight: '700', marginBottom: 8 },
  card: {
    borderWidth: StyleSheet.hairlineWidth,
    borderColor: '#cbd5e1',
    borderRadius: 8,
    padding: 10,
    marginTop: 10,
    backgroundColor: '#fff',
  },
  statusCard: {
    borderWidth: StyleSheet.hairlineWidth,
    borderColor: '#cbd5e1',
    borderRadius: 8,
    padding: 10,
    backgroundColor: '#fff',
  },
  sectionLabel: { fontWeight: '700', fontSize: 15, marginBottom: 6 },
  statusBadge: { alignSelf: 'flex-start', borderRadius: 999, paddingHorizontal: 12, paddingVertical: 5 },
  statusText: { fontWeight: '800', fontSize: 13 },
  sourceNote: { color: '#64748b', fontSize: 11, marginTop: 6 },
  note: { color: '#475569', fontSize: 12, marginTop: 4, lineHeight: 17 },
  heading: { fontWeight: '700', marginTop: 12, marginBottom: 4, fontSize: 14 },
  body: { fontSize: 13, color: '#334155', lineHeight: 18, marginBottom: 2 },
  pendingBox: { backgroundColor: '#fdf6e3', borderRadius: 8, padding: 10, marginTop: 10 },
  pendingText: { color: '#8a5a00', fontSize: 12, lineHeight: 17 },
  syncRow: { marginTop: 8, borderTopWidth: StyleSheet.hairlineWidth, borderTopColor: '#e2e8f0', paddingTop: 8 },
  syncLabel: { fontWeight: '600', fontSize: 13 },
  syncMeta: { color: '#64748b', fontSize: 11, marginTop: 2 },
  button: { backgroundColor: '#0a3069', borderRadius: 8, paddingHorizontal: 14, paddingVertical: 10, marginTop: 10 },
  buttonText: { color: '#fff', fontWeight: '700', fontSize: 13, textAlign: 'center' },
  refreshButton: { alignSelf: 'center', marginTop: 14, padding: 8 },
  refreshText: { color: '#1f6feb', fontSize: 13 },
});
