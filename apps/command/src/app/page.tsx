import { commandConfig } from "@/lib/config";
import { requireStaffPage } from "@/lib/auth/page-guard";
import { fetchJobs, JOB_STATUSES, type JobStatus } from "@/lib/api/jobs";
import { fetchDispatchFactors } from "@/lib/api/dispatch";
import { buildEscalations, fetchEscalationPages, type EscalationRow } from "@/lib/escalations";
import { probeSystemHealth, type ServiceHealth } from "@/lib/api/health";
import { UpstreamError } from "@/lib/api/upstream";
import { maskIdentifier } from "@/lib/pii/mask";
import { audit } from "@/lib/audit/audit";
import { ForbiddenScreen } from "@/components/forbidden-screen";
import { SiteHeader } from "@/components/site-header";
import { SiteFooter } from "@/components/site-footer";
import { AutoRefresh } from "@/components/auto-refresh";
import { SessionKeepAlive } from "@/components/session-keep-alive";
import { JobBoardPanel, type JobBoardFilters } from "@/components/panels/job-board-panel";
import { EscalationsPanel } from "@/components/panels/escalations-panel";
import { SystemHealthPanel } from "@/components/panels/system-health-panel";
import { IncidentsPanel } from "@/components/panels/incidents-panel";
import type { PanelError } from "@/components/panels/panel-primitives";

/*
 * The console page (issue #123). Server component: every panel's data is
 * fetched here, server-side, with the operator's own verified short-lived
 * token — the browser bundle holds no tokens and calls no backend. Each
 * panel loads independently (Promise.all + per-panel catch) so a degraded
 * backend degrades its panel only.
 *
 * Two-layer enforcement: the proxy already admitted this request, and
 * requireStaffPage() re-verifies the session before anything renders.
 */

export const dynamic = "force-dynamic";

type SearchParams = Promise<Record<string, string | string[] | undefined>>;

function firstParam(value: string | string[] | undefined): string {
  if (Array.isArray(value)) {
    return value[0] ?? "";
  }
  return value ?? "";
}

function normalizeFilters(params: Record<string, string | string[] | undefined>): JobBoardFilters {
  const rawStatus = firstParam(params.status).toUpperCase();
  const status = (JOB_STATUSES as readonly string[]).includes(rawStatus)
    ? (rawStatus as JobStatus)
    : "ALL";
  return { status, technician: firstParam(params.technician).trim() };
}

async function safe<T>(load: () => Promise<T>): Promise<{ data: T | null; error: PanelError | null }> {
  try {
    return { data: await load(), error: null };
  } catch (error) {
    if (error instanceof UpstreamError) {
      return {
        data: null,
        error: { code: error.code, title: error.title, unreachable: error.status === 0 },
      };
    }
    return {
      data: null,
      error: { code: "unexpected_error", title: "The panel failed to load", unreachable: false },
    };
  }
}

export default async function ConsolePage({ searchParams }: { searchParams: SearchParams }) {
  const session = await requireStaffPage();
  if (session === null) {
    return <ForbiddenScreen />;
  }

  const params = await searchParams;
  const filters = normalizeFilters(params);
  const config = commandConfig();
  const now = new Date();
  const generatedAt = now.toISOString().replace("T", " ").slice(0, 19) + " UTC";

  // Audit the view BEFORE any data renders: the operator is about to look
  // at customer-linked operational data (issue #123 PII acceptance).
  audit({
    action: "console.view",
    actor: session.claims.sub,
    route: "/",
    detail: {
      status: filters.status,
      technician: filters.technician === "" ? null : maskIdentifier(filters.technician, "technician"),
    },
  });

  const token = session.accessToken;

  const [board, escalations, health] = await Promise.all([
    (async () => {
      const [jobs, factors] = await Promise.all([
        safe(() =>
          fetchJobs(
            config.jobsUrl,
            {
              status: filters.status === "ALL" ? undefined : filters.status,
              technicianId: filters.technician === "" ? undefined : filters.technician,
              limit: 200,
            },
            token,
            config.upstreamTimeoutMs,
          ),
        ),
        safe(() =>
          fetchDispatchFactors(config.dispatchUrl, token, config.upstreamTimeoutMs),
        ),
      ]);
      return { jobs, factors };
    })(),
    (async () => {
      const attempt = await safe(() =>
        fetchEscalationPages(config.jobsUrl, token, config.upstreamTimeoutMs),
      );
      if (attempt.error !== null) {
        return { rows: null as EscalationRow[] | null, error: attempt.error, failedStatuses: [] as JobStatus[] };
      }
      const pages = attempt.data!;
      return {
        rows: buildEscalations(pages.pages, now),
        error: null,
        failedStatuses: pages.failedStatuses,
      };
    })(),
    safe(() =>
      probeSystemHealth(
        {
          identity: config.identityUrl,
          vehicles: config.vehiclesUrl,
          jobs: config.jobsUrl,
          dispatch: config.dispatchUrl,
          inspections: config.inspectionsUrl,
          payments: config.paymentsUrl,
        },
        config.upstreamTimeoutMs,
      ),
    ),
  ]);

  const environment =
    process.env.NODE_ENV === "production" ? "production" : `dev · ${config.apiBaseUrl === "" ? "host defaults" : "gateway"}`;

  return (
    <div className="flex min-h-screen flex-col">
      <SiteHeader actorSub={session.claims.sub} role={session.claims.role} environment={environment} />
      <main
        id="main-content"
        className="mx-auto w-full max-w-7xl flex-1 space-y-6 px-4 py-6 sm:px-6"
      >
        <JobBoardPanel
          jobs={board.jobs.data}
          error={board.jobs.error}
          factors={board.factors.data}
          factorsError={board.factors.error}
          filters={filters}
          generatedAt={generatedAt}
        />
        <EscalationsPanel
          rows={escalations.rows}
          error={escalations.error}
          failedStatuses={escalations.failedStatuses}
          generatedAt={generatedAt}
        />
        <SystemHealthPanel
          services={(health.data as ServiceHealth[] | null) ?? null}
          error={health.error}
          checkedAt={generatedAt}
        />
        <IncidentsPanel />
        <AutoRefresh seconds={config.autoRefreshSeconds} />
      </main>
      <SiteFooter />
      <SessionKeepAlive intervalSeconds={Math.min(Math.max(config.autoRefreshSeconds * 3, 300), 600)} />
    </div>
  );
}
