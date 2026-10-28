import Link from "next/link";
import { JOB_STATUSES, type JobRecord, type JobStatus } from "@/lib/api/jobs";
import { maskCustomerId, maskIdentifier, maskTechnicianId } from "@/lib/pii/mask";
import { formatTimestamp } from "@/lib/format";
import { StatusBadge } from "@/components/status-badge";
import { JOB_LIST_TRACKING_ISSUE, JOB_LIST_TRACKING_LABEL } from "./trackers";
import {
  PanelEmptyState,
  PanelErrorState,
  PanelSection,
  type PanelError,
} from "./panel-primitives";

/*
 * Garage job board (issue #124 panel 1): jobs assigned to this garage.
 *
 * Wired to the EXISTING GET /v1/jobs (contracts/jobs/openapi.yaml). The
 * endpoint currently answers 403 for the GARAGE role (its list route is
 * RBAC-gated to DISPATCHER/ADMIN/SUPER_ADMIN — backend/jobs/http.go), so
 * in production this panel renders its honest degraded state explaining
 * the RBAC gap; the moment the backend admits the garage role with
 * tenant-scoped reads (tracked in the child issue), the panel goes live
 * with zero console changes. No garage/tenant parameter exists on this
 * panel — the backend scopes whatever it returns from the token claims.
 */

export interface JobBoardFilters {
  status: JobStatus | "ALL";
  technician: string;
}

export interface JobBoardPanelProps {
  jobs: JobRecord[] | null;
  error: PanelError | null;
  filters: JobBoardFilters;
  generatedAt: string;
}

export function JobBoardPanel(props: JobBoardPanelProps) {
  const { jobs, error, filters, generatedAt } = props;
  const activeFilter = filters.status !== "ALL" || filters.technician.trim() !== "";
  const rbacGap = error !== null && error.status === 403;

  return (
    <PanelSection
      id="job-board"
      title="Job board"
      description="Jobs at this garage — filter by status or technician. Auto-refreshes; identifiers are masked per the PII policy."
      status={<span data-testid="board-updated">Data as of {generatedAt}</span>}
    >
      <form
        method="get"
        action="/"
        aria-label="Job board filters"
        className="flex flex-wrap items-end gap-3 rounded-md border border-slate-200 bg-slate-50 p-3"
      >
        <div className="flex flex-col gap-1">
          <label htmlFor="filter-status" className="text-xs font-semibold text-slate-600">
            Status
          </label>
          <select
            id="filter-status"
            name="status"
            defaultValue={filters.status}
            className="min-h-11 rounded-md border border-slate-300 bg-white px-3 py-2 text-sm text-slate-800"
          >
            <option value="ALL">All statuses</option>
            {JOB_STATUSES.map((status) => (
              <option key={status} value={status}>
                {status.replace(/_/g, " ")}
              </option>
            ))}
          </select>
        </div>
        <div className="flex flex-col gap-1">
          <label htmlFor="filter-technician" className="text-xs font-semibold text-slate-600">
            Technician ID
          </label>
          <input
            id="filter-technician"
            name="technician"
            type="text"
            inputMode="text"
            placeholder="UUID (optional)"
            defaultValue={filters.technician}
            className="min-h-11 w-64 rounded-md border border-slate-300 bg-white px-3 py-2 text-sm text-slate-800 placeholder:text-slate-400"
          />
        </div>
        <button
          type="submit"
          className="min-h-11 rounded-md bg-amber-700 px-4 py-2 text-sm font-semibold text-white hover:bg-amber-800"
        >
          Apply filters
        </button>
        {activeFilter ? (
          <Link
            href="/"
            className="min-h-11 inline-flex items-center rounded-md border border-slate-300 bg-white px-4 py-2 text-sm font-semibold text-slate-700 hover:bg-slate-100"
          >
            Reset
          </Link>
        ) : null}
      </form>

      <div className="mt-4">
        {rbacGap ? (
          <div
            role="status"
            className="rounded-md border border-slate-200 bg-slate-50 p-4 text-sm leading-relaxed text-slate-600"
          >
            <p className="font-semibold text-slate-800">
              Backend capability pending — the jobs API does not yet admit the GARAGE role
            </p>
            <p className="mt-2">
              The job list endpoint exists (GET /v1/jobs) but is currently restricted to Motivra
              operator roles, so the garage board cannot list jobs yet (your sign-in answered
              403). No workaround is built in: this console never reads other garages&rsquo; data
              and does not impersonate operator roles.
            </p>
            <p className="mt-2">
              Tracked in{" "}
              <a
                className="font-semibold text-amber-800 underline underline-offset-4"
                href={JOB_LIST_TRACKING_ISSUE}
                rel="noreferrer"
              >
                {JOB_LIST_TRACKING_LABEL}
              </a>
              . When the garage-scoped read ships, this board goes live with no console change.
            </p>
          </div>
        ) : error !== null ? (
          <PanelErrorState
            error={error}
            hint="The jobs service may be down or restarting. Every other panel keeps working; this board resumes on the next refresh."
          />
        ) : jobs === null || jobs.length === 0 ? (
          <PanelEmptyState
            message={
              activeFilter
                ? "No jobs match the current filters."
                : "No jobs are assigned to this garage yet. The board fills as Motivra dispatches work to you."
            }
          />
        ) : (
          <div className="console-scroll max-h-96 overflow-y-auto rounded-md border border-slate-200">
            <table className="min-w-full divide-y divide-slate-200 text-sm">
              <caption className="sr-only">
                Jobs with status, problem summary, masked technician and customer references and
                timestamps
              </caption>
              <thead className="sticky top-0 bg-slate-100 text-left text-xs font-semibold uppercase tracking-wide text-slate-600">
                <tr>
                  <th scope="col" className="px-3 py-2">Job</th>
                  <th scope="col" className="px-3 py-2">Status</th>
                  <th scope="col" className="px-3 py-2">Problem summary</th>
                  <th scope="col" className="px-3 py-2">Technician</th>
                  <th scope="col" className="px-3 py-2">Customer</th>
                  <th scope="col" className="px-3 py-2">Created</th>
                  <th scope="col" className="px-3 py-2">Updated</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-100 bg-white">
                {jobs.map((job) => (
                  <tr key={job.id} className="hover:bg-slate-50">
                    <td className="px-3 py-2 font-mono text-xs text-slate-500">
                      {maskIdentifier(job.id, "job")}
                    </td>
                    <td className="px-3 py-2">
                      <StatusBadge status={job.status} />
                    </td>
                    <td className="max-w-72 truncate px-3 py-2 text-slate-800" title={job.problem_summary}>
                      {job.problem_summary}
                    </td>
                    <td className="px-3 py-2 font-mono text-xs text-slate-500">
                      {maskTechnicianId(job.technician_id)}
                    </td>
                    <td className="px-3 py-2 font-mono text-xs text-slate-500">
                      {maskCustomerId(job.customer_id)}
                    </td>
                    <td className="px-3 py-2 text-xs text-slate-500">{formatTimestamp(job.created_at)}</td>
                    <td className="px-3 py-2 text-xs text-slate-500">{formatTimestamp(job.updated_at)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
    </PanelSection>
  );
}
