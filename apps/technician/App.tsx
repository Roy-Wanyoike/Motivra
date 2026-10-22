import { useMemo } from 'react';
import { Pressable, SafeAreaView, StyleSheet, Text, View } from 'react-native';

import { NavigationProvider, useNavigation } from './src/navigation/NavigationContext';
import { JobListScreen } from './src/screens/JobListScreen';
import { JobDetailScreen } from './src/screens/JobDetailScreen';
import { OnboardingScreen } from './src/screens/OnboardingScreen';
import { ProfileScreen } from './src/screens/ProfileScreen';
import { EarningsScreen } from './src/screens/EarningsScreen';
import { SyncBanner } from './src/components/SyncBanner';
import { SyncProvider } from './src/sync/SyncContext';
import { TechnicianPipelineProvider } from './src/pipeline/TechnicianPipelineContext';
import { JobStore } from './src/state/job-store';
import { MOCK_JOBS, SimulatedJobsApi } from './src/mocks/jobs';
import type { PullFn, PullPage } from './src/sync/api';
import type { Route } from './src/navigation/NavigationContext';

/**
 * Motivra Tech — app shell.
 *
 * Demo-data policy (production readiness): the simulated API and its mock
 * job data are DEVELOPMENT fixtures. They are wired only when
 * EXPO_PUBLIC_USE_MOCKS=1 is set at build time. Without that flag the jobs
 * surfaces render an honest "transport not connected" state — no demo jobs,
 * no simulated writes — so a production build can never display or accept
 * fake data.
 *
 * Technician pipeline (issue #126 Phase 1): onboarding wizard, profile and
 * earnings screens are NOT demo data — they run in both modes, collect real
 * on-device state (skills-profile drafts queued through the pipeline
 * outbox) and degrade honestly ("backend pending") for reads. They live in
 * their own provider, separate from the jobs sync stack.
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

function Router({ jobStore }: { jobStore: JobStore | null }) {
  const { route } = useNavigation();
  switch (route.name) {
    case 'JobList':
      return jobStore ? <JobListScreen jobStore={jobStore} /> : <TransportNotConnected />;
    case 'JobDetail':
      return jobStore ? (
        <JobDetailScreen jobId={route.params.jobId} jobStore={jobStore} />
      ) : (
        <TransportNotConnected />
      );
    case 'Onboarding':
      return <OnboardingScreen />;
    case 'Profile':
      return <ProfileScreen />;
    case 'Earnings':
      return <EarningsScreen />;
  }
}

/**
 * Production default: no demo data anywhere. States the missing transport
 * plainly instead of simulating one, and points at the pipeline screens
 * that DO work on-device in this build.
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
        Technician pipeline screens (onboarding, profile, earnings) run
        on-device in this build — switch with the tabs below.
      </Text>
      <Text style={styles.disconnectedMeta}>
        Development builds can run the simulated transport with
        EXPO_PUBLIC_USE_MOCKS=1.
      </Text>
    </View>
  );
}

const NAV_TABS: { label: string; route: Route }[] = [
  { label: 'Jobs', route: { name: 'JobList' } },
  { label: 'Onboard', route: { name: 'Onboarding' } },
  { label: 'Profile', route: { name: 'Profile' } },
  { label: 'Earnings', route: { name: 'Earnings' } },
];

/** Persistent bottom nav (same tiny typed navigation context, no new library). */
function PipelineNavRow() {
  const { route, navigate } = useNavigation();
  return (
    <View style={styles.navRow}>
      {NAV_TABS.map((tab) => {
        const active = route.name === tab.route.name;
        return (
          <Pressable
            key={tab.route.name}
            testID={`nav-${tab.route.name}`}
            style={[styles.navButton, active && styles.navButtonActive]}
            onPress={() => navigate(tab.route)}
          >
            <Text style={[styles.navLabel, active && styles.navLabelActive]}>{tab.label}</Text>
          </Pressable>
        );
      })}
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

  return (
    <NavigationProvider>
      <TechnicianPipelineProvider>
        <SafeAreaView style={styles.safe}>
          {USE_MOCKS ? (
            <SyncProvider dispatcher={stubDispatcher} pull={stubPull} store={jobStore}>
              <SyncBanner />
              <Router jobStore={jobStore} />
              <PipelineNavRow />
            </SyncProvider>
          ) : (
            <>
              <Router jobStore={null} />
              <PipelineNavRow />
            </>
          )}
          <Text style={styles.footer}>
            {USE_MOCKS
              ? 'Motivra Tech 0.1.0 — foundation build (simulated API, in-memory outbox)'
              : 'Motivra Tech 0.1.0 — jobs transport not connected · pipeline runs on-device (Phase 1)'}
          </Text>
        </SafeAreaView>
      </TechnicianPipelineProvider>
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
  navRow: {
    flexDirection: 'row',
    borderTopWidth: StyleSheet.hairlineWidth,
    borderTopColor: '#e2e8f0',
  },
  navButton: { flex: 1, alignItems: 'center', paddingVertical: 10 },
  navButtonActive: { backgroundColor: '#f1f5f9' },
  navLabel: { fontSize: 12, color: '#64748b', fontWeight: '600' },
  navLabelActive: { color: '#0a3069', fontWeight: '800' },
});
