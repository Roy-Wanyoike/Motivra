import { Pressable, ScrollView, StyleSheet, Text, View } from 'react-native';

import { usePipeline } from '../pipeline/TechnicianPipelineContext';
import { formatKes } from '../pipeline/profile';

/**
 * Earnings screen (issue #126 Phase 1) — wired to the draft contract client
 * (GET /v1/technician/earnings) and honest about Phase 4:
 *
 *   "Earnings sync pending — backend Phase 2/4, tracked #166/#168."
 *
 * When a backend IS configured (EXPO_PUBLIC_MOTIVRA_API_URL), the read runs
 * for real: ledger-backed per-job lines (gross → platform fee → net,
 * integer KES cents) plus the payout-eligible balance. The screen NEVER
 * invents amounts — no demo data (repo principle 10); money is display-
 * formatted only (repo principle 4).
 */
export function EarningsScreen() {
  const { earnings, backendDegraded, refresh } = usePipeline();

  return (
    <ScrollView style={styles.container} testID="earnings-screen">
      <Text style={styles.title}>Earnings</Text>

      {earnings.kind === 'backend-pending' ? (
        <View style={styles.pendingCard} testID="earnings-pending">
          <Text style={styles.pendingTitle}>Earnings sync pending</Text>
          <Text style={styles.pendingBody}>
            The technician earnings backend is not connected in this build —
            it lands with the identity verification work (Phase 2, issue
            #166) and the ledger-backed earnings + M-Pesa payout path (Phase
            4, issue #168).
          </Text>
          <Text style={styles.pendingBody}>
            When it connects, this screen shows, per completed job:
          </Text>
          <View style={styles.previewBox}>
            <Text style={styles.previewRow}>Gross (what the customer paid)</Text>
            <Text style={styles.previewRow}>Platform fee (always visible)</Text>
            <Text style={styles.previewRow}>Net (what you keep — pays out to M-Pesa)</Text>
          </View>
          <Text style={styles.pendingMeta}>
            Amounts come straight from the payments ledger in integer KES —
            the app displays them; it never computes money.
          </Text>
        </View>
      ) : null}

      {earnings.kind === 'error' ? (
        <View style={styles.errorCard} testID="earnings-error">
          <Text style={styles.errorTitle}>Could not read earnings</Text>
          <Text style={styles.errorBody}>{earnings.message}</Text>
          <Text style={styles.pendingMeta}>
            The ledger stays authoritative — retry when the connection
            returns. Nothing you see here is an estimate.
          </Text>
        </View>
      ) : null}

      {earnings.kind === 'ok' ? (
        <View style={styles.okCard} testID="earnings-ok">
          <Text style={styles.balanceLabel}>Payout-eligible balance</Text>
          <Text style={styles.balanceValue}>{formatKes(earnings.summary.balance_minor)}</Text>
          {earnings.summary.lines.length === 0 ? (
            <Text style={styles.body}>No completed jobs yet — earnings appear after a job is completed and paid.</Text>
          ) : (
            earnings.summary.lines.map((line) => (
              <View key={line.job_id} style={styles.lineRow}>
                <Text style={styles.lineJob}>Job {line.job_id.slice(0, 8)}… · {line.status.replace('_', ' ')}</Text>
                <Text style={styles.lineMoney}>
                  {formatKes(line.gross_minor)} − {formatKes(line.fee_minor)} fee = {formatKes(line.net_minor)} net
                </Text>
                <Text style={styles.lineMeta}>{line.earned_at}</Text>
              </View>
            ))
          )}
          {backendDegraded ? (
            <Text style={styles.pendingMeta}>Mode: degraded (no backend configured) — this read cannot happen.</Text>
          ) : null}
        </View>
      ) : null}

      <Pressable testID="refresh-earnings" style={styles.refreshButton} onPress={() => void refresh()}>
        <Text style={styles.refreshText}>Refresh earnings</Text>
      </Pressable>
      <Text style={styles.footerMeta}>
        Payout path (M-Pesa, Phase 4 — issue #168) will draw only on the net
        balance shown here. Double-pays are prevented server-side by
        idempotency keys; failed payouts surface here and in Command.
      </Text>
    </ScrollView>
  );
}

const styles = StyleSheet.create({
  container: { flex: 1, padding: 12 },
  title: { fontSize: 22, fontWeight: '700', marginBottom: 8 },
  pendingCard: {
    borderWidth: StyleSheet.hairlineWidth,
    borderColor: '#cbd5e1',
    borderRadius: 8,
    padding: 12,
    backgroundColor: '#f8fafc',
  },
  pendingTitle: { fontSize: 16, fontWeight: '700', marginBottom: 6 },
  pendingBody: { fontSize: 13, color: '#334155', lineHeight: 19, marginBottom: 6 },
  pendingMeta: { color: '#64748b', fontSize: 11, marginTop: 8, lineHeight: 15 },
  previewBox: {
    borderWidth: StyleSheet.hairlineWidth,
    borderColor: '#cbd5e1',
    borderRadius: 8,
    padding: 10,
    backgroundColor: '#fff',
    marginVertical: 4,
  },
  previewRow: { fontSize: 13, color: '#0f172a', paddingVertical: 2 },
  errorCard: { backgroundColor: '#fdecea', borderRadius: 8, padding: 12 },
  errorTitle: { color: '#c62828', fontWeight: '700', fontSize: 15, marginBottom: 4 },
  errorBody: { color: '#8b1a12', fontSize: 13 },
  okCard: {
    borderWidth: StyleSheet.hairlineWidth,
    borderColor: '#cbd5e1',
    borderRadius: 8,
    padding: 12,
    backgroundColor: '#fff',
  },
  balanceLabel: { fontSize: 13, color: '#475569' },
  balanceValue: { fontSize: 26, fontWeight: '800', marginBottom: 8 },
  body: { fontSize: 13, color: '#334155', lineHeight: 18 },
  lineRow: { borderTopWidth: StyleSheet.hairlineWidth, borderTopColor: '#e2e8f0', paddingVertical: 8 },
  lineJob: { fontSize: 13, fontWeight: '600' },
  lineMoney: { fontSize: 13, color: '#0f172a', marginTop: 2 },
  lineMeta: { fontSize: 11, color: '#64748b', marginTop: 2 },
  refreshButton: { alignSelf: 'center', marginTop: 14, padding: 8 },
  refreshText: { color: '#1f6feb', fontSize: 13 },
  footerMeta: { color: '#64748b', fontSize: 11, textAlign: 'center', marginTop: 8, lineHeight: 15 },
});
