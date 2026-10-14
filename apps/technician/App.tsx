import { useMemo } from 'react';
import { SafeAreaView, StyleSheet, Text, View } from 'react-native';

import { NavigationProvider, useNavigation } from './src/navigation/NavigationContext';
import { JobListScreen } from './src/screens/JobListScreen';
import { JobDetailScreen } from './src/screens/JobDetailScreen';
import { SyncBanner } from './src/components/SyncBanner';
import { SyncProvider } from './src/sync/SyncContext';
import { JobStore } from './src/state/job-store';
import { MOCK_JOBS, SimulatedJobsApi } from './src/mocks/jobs';
import type { PullFn, PullPage } from './src/sync/api';

/**
 * Motivra Tech — app shell.
 *
 * Demo-data policy (production readiness): the simulated API and its mock
 * job data are DEVELOPMENT fixtures. They are wired only when
 * EXPO_PUBLIC_USE_MOCKS=1 is set at build time. Without that flag the app
 * renders an honest "transport not connected" state — no demo jobs, no
 * simulated writes — so a production build can never display or accept
 * fake data.
 *
 * The transport swap for the real API remains a single-file change (see
 * README): replace stubPull/stubDispatcher and the seed call below.
 */

/** Stub pull: one page with everything, no cursor (real delta endpoint is a contracts follow-up). */
const stubPull: PullFn = async (): Promise<PullPage> => ({
  jobs: MOCK_JOBS,
  nextCursor: null,
});

const stubDispatcher = new SimulatedJobsApi();

const USE_MOCKS = process.env.EXPO_PUBLIC_USE_MOCKS === '1';

function Router({ jobStore }: { jobStore: JobStore }) {
  const { route } = useNavigation();
  switch (route.name) {
    case 'JobList':
      return <JobListScreen jobStore={jobStore} />;
    case 'JobDetail':
      return <JobDetailScreen jobId={route.params.jobId} jobStore={jobStore} />;
  }
}

/**
 * Production default: no demo data anywhere. States the missing transport
 * plainly instead of simulating one.
 */
function TransportNotConnected() {
  return (
    <View style={styles.disconnected}>
      <Text style={styles.disconnectedTitle}>Jobs transport not connected</Text>
      <Text style={styles.disconnectedBody}>
        This build has no jobs API transport wired. When the technician jobs
        endpoints are connected, assigned jobs will appear here and accepted
        work will sync through the outbox.
      </Text>
      <Text style={styles.disconnectedMeta}>
        Development builds can run the simulated transport with
        EXPO_PUBLIC_USE_MOCKS=1.
      </Text>
    </View>
  );
}

export default function App() {
  const jobStore = useMemo(() => {
    const store = new JobStore();
    if (USE_MOCKS) {
      store.seed(MOCK_JOBS);
    }
    return store;
  }, []);

  if (!USE_MOCKS) {
    return (
      <SafeAreaView style={styles.safe}>
        <TransportNotConnected />
        <Text style={styles.footer}>
          Motivra Tech 0.1.0 — foundation build (no transport connected)
        </Text>
      </SafeAreaView>
    );
  }

  return (
    <NavigationProvider>
      <SyncProvider dispatcher={stubDispatcher} pull={stubPull} store={jobStore}>
        <SafeAreaView style={styles.safe}>
          <SyncBanner />
          <Router jobStore={jobStore} />
          <Text style={styles.footer}>
            Motivra Tech 0.1.0 — foundation build (simulated API, in-memory outbox)
          </Text>
        </SafeAreaView>
      </SyncProvider>
    </NavigationProvider>
  );
}

const styles = StyleSheet.create({
  safe: { flex: 1, backgroundColor: '#fff' },
  footer: { textAlign: 'center', color: '#999', fontSize: 11, paddingVertical: 6 },
  disconnected: { flex: 1, alignItems: 'center', justifyContent: 'center', padding: 24 },
  disconnectedTitle: { fontSize: 18, fontWeight: '700', color: '#0f172a', marginBottom: 8 },
  disconnectedBody: { fontSize: 14, color: '#475569', textAlign: 'center', lineHeight: 20 },
  disconnectedMeta: {
    fontSize: 12,
    color: '#94a3b8',
    textAlign: 'center',
    marginTop: 12,
  },
});
