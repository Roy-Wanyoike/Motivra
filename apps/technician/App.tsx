import { useMemo } from 'react';
import { SafeAreaView, StyleSheet, Text } from 'react-native';

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
 * Wiring is intentionally transparent: a simulated API (src/mocks) stands in
 * for the real jobs transport, so the outbox → drain → pull loop is runnable
 * today and the transport swap is a single-file change (see README).
 */

/** Stub pull: one page with everything, no cursor (real delta endpoint is a contracts follow-up). */
const stubPull: PullFn = async (): Promise<PullPage> => ({
  jobs: MOCK_JOBS,
  nextCursor: null,
});

const stubDispatcher = new SimulatedJobsApi();

function Router({ jobStore }: { jobStore: JobStore }) {
  const { route } = useNavigation();
  switch (route.name) {
    case 'JobList':
      return <JobListScreen jobStore={jobStore} />;
    case 'JobDetail':
      return <JobDetailScreen jobId={route.params.jobId} jobStore={jobStore} />;
  }
}

export default function App() {
  const jobStore = useMemo(() => {
    const store = new JobStore();
    store.seed(MOCK_JOBS);
    return store;
  }, []);

  return (
    <NavigationProvider>
      <SyncProvider dispatcher={stubDispatcher} pull={stubPull} store={jobStore}>
        <SafeAreaView style={styles.safe}>
          <SyncBanner />
          <Router jobStore={jobStore} />
          <Text style={styles.footer}>
            Motivra Tech 0.1.0 — foundation build (stub API, in-memory outbox)
          </Text>
        </SafeAreaView>
      </SyncProvider>
    </NavigationProvider>
  );
}

const styles = StyleSheet.create({
  safe: { flex: 1, backgroundColor: '#fff' },
  footer: { textAlign: 'center', color: '#999', fontSize: 11, paddingVertical: 6 },
});
