import Link from "next/link";
import type { FactorCatalogue } from "@/lib/api/dispatch";
import { JOB_STATUSES, type JobRecord, type JobStatus } from "@/lib/api/jobs";
import { maskCustomerId, maskIdentifier, maskTechnicianId } from "@/lib/pii/mask";
import { formatTimestamp } from "@/lib/format";
import { StatusBadge } from "@/components/status-badge";
import {
  PanelEmptyState,
  PanelErrorState,
  PanelSection,
  type PanelError,
} from "./panel-primitives";

/*
 * Live job board (issue #123 panel 1): every job with its current state,
 * filterable by status and technician, reading GET /v1/jobs (contracts/
 * jobs/openapi.yaml). Identifiers are masked by default (PII-HANDLING.md);
 * the dispatch factor catalogue (GET /v1/dispatch/factors) is rendered
 * beside the board so the ranking engine stays explainable to operators.
 */

export interface JobBoardFilters {
  status: JobStatus | "ALL";
  technician: string;
}

export interface JobBoardPanelProps {
  jobs: JobRecord[] | null;
  error: PanelError | null;
  factors: FactorCatalogue | null;
  factorsError: PanelError | null;
  filters: JobBoardFilters;
  generatedAt: string;
}

export function JobBoardPanel(props: JobBoardPanelProps) {
  const { jobs, error, factors, factorsError, filters, generatedAt } = props;
  const activeFilter = filters.status !== "ALL" || filters.technician.trim() !== "";

  return (
    <PanelSection
      id="job-board"
      title="Job board"
      description="Every job with its current state — filter by status or technician. Auto-refreshes; identifiers are masked per the PII policy."
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
        {error !== null ? (
          <PanelErrorState
            error={error}
            hint="The jobs service may be down or restarting. Every other panel keeps working; this board resumes on the next refresh."
          />
        ) : jobs === null || jobs.length === 0 ? (
          <PanelEmptyState
            message={
              activeFilter
                ? "No jobs match the current filters."
                : "No jobs exist yet. The board fills as customers create service requests."
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

      <div className="mt-4 rounded-md border border-slate-200 p-3">
        <h3 className="text-xs font-semibold uppercase tracking-wide text-slate-600">
          Dispatch scoring factors (explainable ranking)
        </h3>
        {factorsError !== null ? (
          <p className="mt-2 text-sm text-slate-500">
            Factor catalogue unavailable right now ({factorsError.code}) — the board itself is
            unaffected.
          </p>
        ) : factors === null ? null : (
          <ul className="mt-2 flex flex-wrap gap-2">
            {factors.factors.map((factor) => (
              <li
                key={factor.name}
                title={factor.description}
                className="rounded-full bg-slate-100 px-3 py-1 text-xs text-slate-700"
              >
                <span className="font-semibold">{factor.name.replace(/_/g, " ")}</span>{" "}
                <span className="text-slate-500">weight {factor.default_weight}</span>
              </li>
            ))}
          </ul>
        )}
      </div>
    </PanelSection>
  );
}
