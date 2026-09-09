import { Pressable, StyleSheet, Text, View } from 'react-native';

import { useSync } from '../sync/SyncContext';
import type { SyncStatus } from '../sync/SyncContext';

/**
 * Global sync status banner — the offline-first surface of the app.
 * Shows connection state (inferred from the last sync attempt, NOT from real
 * connectivity detection — netinfo is a follow-up) plus the pending op count.
 */
export function SyncBanner() {
  const { status, pendingCount, syncNow } = useSync();

  const label: Record<SyncStatus, string> = {
    idle: 'SYNC: idle',
    syncing: 'SYNC: syncing…',
    ok: 'SYNCED',
    error: 'OFFLINE — sync failed',
  };

  const style = styles[status];

  return (
    <Pressable onPress={() => void syncNow()} testID="sync-banner">
      <View style={[styles.banner, style]}>
        <Text style={styles.text}>
          {label[status]}
          {pendingCount > 0 ? ` · ${pendingCount} queued op${pendingCount === 1 ? '' : 's'}` : ''}
        </Text>
        <Text style={styles.hint}>tap to sync</Text>
      </View>
    </Pressable>
  );
}

const styles = StyleSheet.create({
  banner: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
    paddingVertical: 8,
    paddingHorizontal: 12,
  },
  text: { color: '#fff', fontWeight: '600', fontSize: 12 },
  hint: { color: '#ffffffaa', fontSize: 11 },
  idle: { backgroundColor: '#555' },
  syncing: { backgroundColor: '#1f6feb' },
  ok: { backgroundColor: '#1a7f37' },
  error: { backgroundColor: '#b35900' },
});
