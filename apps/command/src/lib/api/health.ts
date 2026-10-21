/*
 * System-health aggregation for the Command health panel.
 *
 * Every cmd/* service exposes unauthenticated GET /healthz (liveness) and
 * GET /readyz (dependency checks) via the platform server conventions
 * (backend/platform/server.go): healthz answers {"status":"ok"}; readyz
 * answers {"status":"ready"} or 503 problem+json with the FAILED CHECKER
 * NAMES ONLY (error details are deliberately not exposed, hardening #81).
 *
 * Green/amber/red: green = live + ready; amber = live but not ready
 * (degraded); red = not live or unreachable. Each probe is independent —
 * one dead service never fails the panel.
 */

export const HEALTH_SERVICES = [
  "identity",
  "vehicles",
  "jobs",
  "dispatch",
  "inspections",
  "payments",
] as const;

export type HealthService = (typeof HEALTH_SERVICES)[number];
export type HealthState = "green" | "amber" | "red";
export type ProbeOutcome = "ok" | "not-ready" | "unreachable";

export interface ProbeResult {
  outcome: ProbeOutcome;
  /** Failed checker names from a 503 /readyz body (names only, no detail). */
  failed?: string[];
  latencyMs: number;
}

export interface ServiceHealth {
  service: HealthService;
  state: HealthState;
  healthz: ProbeResult;
  readyz: ProbeResult;
}

/** Pure classification — the unit-tested core of the panel. */
export function classifyServiceHealth(
  healthz: Pick<ProbeResult, "outcome">,
  readyz: Pick<ProbeResult, "outcome">,
): HealthState {
  if (healthz.outcome === "ok" && readyz.outcome === "ok") {
    return "green";
  }
  if (healthz.outcome === "ok" && readyz.outcome === "not-ready") {
    return "amber";
  }
  return "red";
}

export type ServiceUrls = Record<HealthService, string>;

async function probe(
  url: string,
  timeoutMs: number,
): Promise<ProbeResult> {
  const started = Date.now();
  try {
    const response = await fetch(url, {
      cache: "no-store",
      signal: AbortSignal.timeout(timeoutMs),
    });
    const latencyMs = Date.now() - started;
    if (response.ok) {
      return { outcome: "ok", latencyMs };
    }
    if (response.status === 503) {
      const body = (await response.json().catch(() => null)) as
        | { failed?: unknown }
        | null;
      const failed = Array.isArray(body?.failed)
        ? body.failed.filter((entry): entry is string => typeof entry === "string")
        : undefined;
      return { outcome: "not-ready", failed, latencyMs };
    }
    return { outcome: "unreachable", latencyMs };
  } catch {
    return { outcome: "unreachable", latencyMs: Date.now() - started };
  }
}

/**
 * Probe every service's /healthz + /readyz in parallel. The list is fixed
 * (six Wave 1-5 services); an unreachable service reports red, never an
 * exception.
 */
export async function probeSystemHealth(
  urls: ServiceUrls,
  timeoutMs = 4000,
): Promise<ServiceHealth[]> {
  const results = await Promise.all(
    HEALTH_SERVICES.map(async (service): Promise<ServiceHealth> => {
      const base = urls[service].replace(/\/+$/, "");
      const [healthz, readyz] = await Promise.all([
        probe(`${base}/healthz`, timeoutMs),
        probe(`${base}/readyz`, timeoutMs),
      ]);
      return {
        service,
        state: classifyServiceHealth(healthz, readyz),
        healthz,
        readyz,
      };
    }),
  );
  return results;
}
