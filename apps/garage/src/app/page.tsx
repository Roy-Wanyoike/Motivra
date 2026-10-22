import { garageConfig } from "@/lib/config";
import { requireGaragePage } from "@/lib/auth/page-guard";
import { garageIdentityFromClaims } from "@/lib/auth/garage";
import { fetchJobs, JOB_STATUSES, type JobStatus } from "@/lib/api/jobs";
import {
  fetchPaymentIntents,
  fetchIntentTransactions,
  isUuid,
} from "@/lib/api/payments";
import { UpstreamError } from "@/lib/api/upstream";
import { maskIdentifier } from "@/lib/pii/mask";
import { audit } from "@/lib/audit/audit";
import { ForbiddenScreen } from "@/components/forbidden-screen";
import { SiteHeader } from "@/components/site-header";
import { SiteFooter } from "@/components/site-footer";
import { AutoRefresh } from "@/components/auto-refresh";
import { SessionKeepAlive } from "@/components/session-keep-alive";
import { JobBoardPanel, type JobBoardFilters } from "@/components/panels/job-board-panel";
import { AppointmentsPanel } from "@/components/panels/appointments-panel";
import { CapacityPanel } from "@/components/panels/capacity-panel";
import { InvoicesPanel } from "@/components/panels/invoices-panel";
import type { PanelError } from "@/components/panels/panel-primitives";

/*
 * The Garage console page (issue #124). Server component: every panel's
 * data is fetched here, server-side, with the staff member's own verified
 * short-lived token — the browser bundle holds no tokens and calls no
 * backend. Each panel loads independently (Promise.all + per-panel catch)
 * so a degraded backend degrades its panel only.
 *
 * MULTI-TENANT ISOLATION (issue #124's headline requirement): the garage
 * identity is EXCLUSIVELY the verified token's tenant_id claim — see
 * src/lib/auth/garage.ts. No panel accepts a garage/tenant parameter from
 * the URL or anywhere else, and the upstream calls carry no garage
 * identifier at all: the services derive the data boundary from the same
 * token claims (backend/jobs/scope.go, backend/payments/scope.go). A
 * GARAGE session without a tenant claim is admitted but every data panel
 * shows its empty/degraded state — fail closed, never a prompt to type a
 * garage in.
 *
 * Two-layer enforcement: the proxy already admitted this request, and
 * requireGaragePage() re-verifies the session before anything renders.
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

function normalizeIntentSelection(
  params: Record<string, string | string[] | undefined>,
): string | null {
  const raw = firstParam(params.intent).trim();
  // Only a well-formed UUID may select an intent; anything else is ignored
  // (the drill-down never widens scope — the backend re-checks ownership).
  return isUuid(raw) ? raw.toLowerCase() : null;
}

async function safe<T>(load: () => Promise<T>): Promise<{ data: T | null; error: PanelError | null }> {
  try {
    return { data: await load(), error: null };
  } catch (error) {
    if (error instanceof UpstreamError) {
      return {
        data: null,
        error: {
          code: error.code,
          title: error.title,
          unreachable: error.status === 0,
          status: error.status,
        },
      };
    }
    return {
      data: null,
      error: { code: "unexpected_error", title: "The panel failed to load", unreachable: false },
    };
  }
}

export default async function GarageConsolePage({ searchParams }: { searchParams: SearchParams }) {
  const session = await requireGaragePage();
  if (session === null) {
    return <ForbiddenScreen />;
  }

  const params = await searchParams;
  const filters = normalizeFilters(params);
  const selectedIntentId = normalizeIntentSelection(params);
  const config = garageConfig();
  const identity = garageIdentityFromClaims(session.claims);
  const now = new Date();
  const generatedAt = now.toISOString().replace("T", " ").slice(0, 19) + " UTC";

  // Audit the view BEFORE any data renders: the staff member is about to
  // look at customer-linked operational data (PII-HANDLING.md).
  audit({
    action: "garage.console.view",
    actor: session.claims.sub,
    route: "/",
    detail: {
      status: filters.status,
      technician: filters.technician === "" ? null : maskIdentifier(filters.technician, "technician"),
      intent: selectedIntentId === null ? null : maskIdentifier(selectedIntentId, "intent"),
      garage_bound: identity.bound,
    },
  });

  const token = session.accessToken;

  const [board, ledger] = await Promise.all([
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
    // The ledger read is NOT gated on identity.bound: with no tenant claim
    // the payments scope simply matches nothing (fail closed server-side),
    // and the panel's empty state is the honest result.
    safe(() =>
      fetchPaymentIntents(
        config.paymentsUrl,
        { limit: 100 },
        token,
        config.upstreamTimeoutMs,
      ),
    ).then(async (intents) => {
      if (selectedIntentId === null || intents.error !== null) {
        return { intents, transactions: { data: null, error: null } as { data: null; error: PanelError | null } };
      }
      const transactions = await safe(() =>
        fetchIntentTransactions(config.paymentsUrl, selectedIntentId, token, config.upstreamTimeoutMs),
      );
      return { intents, transactions };
    }),
  ]);

  const environment =
    process.env.NODE_ENV === "production" ? "production" : `dev · ${config.apiBaseUrl === "" ? "host defaults" : "gateway"}`;

  return (
    <div className="flex min-h-screen flex-col">
      <SiteHeader
        actorSub={session.claims.sub}
        role={session.claims.role}
        garageTenantId={identity.tenantId}
        environment={environment}
      />
      <main
        id="main-content"
        className="mx-auto w-full max-w-7xl flex-1 space-y-6 px-4 py-6 sm:px-6"
      >
        <JobBoardPanel
          jobs={board.data}
          error={board.error}
          filters={filters}
          generatedAt={generatedAt}
        />
        <InvoicesPanel
          intents={ledger.intents.data}
          error={ledger.intents.error}
          selectedIntentId={selectedIntentId}
          transactions={ledger.transactions.data}
          transactionsError={ledger.transactions.error}
          generatedAt={generatedAt}
        />
        <AppointmentsPanel />
        <CapacityPanel />
        <AutoRefresh seconds={config.autoRefreshSeconds} />
      </main>
      <SiteFooter />
      <SessionKeepAlive intervalSeconds={Math.min(Math.max(config.autoRefreshSeconds * 3, 300), 600)} />
    </div>
  );
}
