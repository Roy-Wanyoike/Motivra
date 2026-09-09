import { createContext, useCallback, useContext, useMemo, useState } from 'react';
import type { ReactNode } from 'react';

/**
 * Tiny typed navigation context — deliberately dependency-free.
 *
 * Upgrade path to expo-router (documented in apps/technician/README.md):
 * this context isolates navigation to two hooks (`useRoute`, `useNavigate`)
 * and two screen components. Moving to expo-router means: (1) `npx expo
 * install expo-router react-native-safe-area-context react-native-screens
 * expo-linking expo-constants expo-status-bar`, (2) wrap with `<Stack>` in
 * app/_layout.tsx, (3) map `JobList` -> `app/(tabs)/index.tsx` and
 * `JobDetail` -> `app/job/[id].tsx`, (4) replace `useNavigate` calls with
 * `router.push` — the route union below already matches those file shapes,
 * so no screen logic changes.
 */

export type Route =
  | { name: 'JobList' }
  | { name: 'JobDetail'; params: { jobId: string } };

export interface NavigationApi {
  route: Route;
  navigate: (route: Route) => void;
  back: () => void;
  /** Simple stack for `back`; bounded to keep memory honest. */
  canGoBack: boolean;
}

const NavigationContext = createContext<NavigationApi | null>(null);

export function NavigationProvider({ children }: { children: ReactNode }) {
  const [stack, setStack] = useState<Route[]>([{ name: 'JobList' }]);

  const navigate = useCallback((route: Route) => {
    setStack((prev) => [...prev.slice(-9), route]);
  }, []);

  const back = useCallback(() => {
    setStack((prev) => (prev.length > 1 ? prev.slice(0, -1) : prev));
  }, []);

  const value = useMemo<NavigationApi>(
    () => ({
      route: stack[stack.length - 1] ?? { name: 'JobList' },
      navigate,
      back,
      canGoBack: stack.length > 1,
    }),
    [stack, navigate, back],
  );

  return <NavigationContext.Provider value={value}>{children}</NavigationContext.Provider>;
}

export function useNavigation(): NavigationApi {
  const ctx = useContext(NavigationContext);
  if (!ctx) throw new Error('useNavigation must be used inside <NavigationProvider>');
  return ctx;
}
