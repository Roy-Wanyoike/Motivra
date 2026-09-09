import { useSyncExternalStore } from 'react';
import { FlatList, Pressable, StyleSheet, Text, View } from 'react-native';

import { useNavigation } from '../navigation/NavigationContext';
import { useSync } from '../sync/SyncContext';
import { JobStore } from '../state/job-store';
import type { JobSnapshot } from '../sync/api';

/**
 * Job list screen — stub data seeded from src/mocks (see README honesty
 * table). The "OFFLINE" badge reflects sync status, not real connectivity.
 */
export function JobListScreen({ jobStore }: { jobStore: JobStore }) {
  const jobs = useSyncExternalStore(jobStore.subscribe, jobStore.getSnapshot);
  const { navigate } = useNavigation();
  const { status, pendingCount } = useSync();

  return (
    <View style={styles.container}>
      <View style={styles.headerRow}>
        <Text style={styles.title}>Jobs</Text>
        {status === 'error' || pendingCount > 0 ? (
          <View style={styles.offlineBadge}>
            <Text style={styles.offlineText}>OFFLINE</Text>
          </View>
        ) : null}
      </View>
      <FlatList
        data={jobs}
        keyExtractor={(job: JobSnapshot) => job.id}
        renderItem={({ item }) => (
          <Pressable
            testID={`job-item-${item.id}`}
            onPress={() => navigate({ name: 'JobDetail', params: { jobId: item.id } })}
            style={styles.row}
          >
            <View style={{ flex: 1 }}>
              <Text style={styles.summary} numberOfLines={2}>
                {item.problem_summary}
              </Text>
              <Text style={styles.meta}>{item.id.slice(0, 8)}…</Text>
            </View>
            <View style={styles.statusPill}>
              <Text style={styles.statusText}>{item.status}</Text>
            </View>
          </Pressable>
        )}
        ListEmptyComponent={<Text style={styles.empty}>No jobs synced yet.</Text>}
      />
    </View>
  );
}

const styles = StyleSheet.create({
  container: { flex: 1, padding: 12 },
  headerRow: { flexDirection: 'row', justifyContent: 'space-between', alignItems: 'center', marginBottom: 8 },
  title: { fontSize: 22, fontWeight: '700' },
  offlineBadge: { backgroundColor: '#b35900', borderRadius: 4, paddingHorizontal: 8, paddingVertical: 3 },
  offlineText: { color: '#fff', fontSize: 11, fontWeight: '700' },
  row: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'space-between',
    padding: 12,
    borderRadius: 8,
    backgroundColor: '#f2f2f7',
    marginBottom: 8,
  },
  summary: { fontSize: 15, fontWeight: '600' },
  meta: { fontSize: 12, color: '#666', marginTop: 2 },
  statusPill: { backgroundColor: '#0a3069', borderRadius: 999, paddingHorizontal: 10, paddingVertical: 4, marginLeft: 8 },
  statusText: { color: '#fff', fontSize: 11, fontWeight: '700' },
  empty: { textAlign: 'center', marginTop: 32, color: '#666' },
});
