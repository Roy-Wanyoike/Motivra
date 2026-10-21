import type { JobStatus } from "@/lib/api/jobs";
import { formatAge, type EscalationRow } from "@/lib/escalations";
import { maskCustomerId, maskIdentifier, maskTechnicianId } from "@/lib/pii/mask";
import { formatTimestamp } from "@/lib/format";
import {
  PanelEmptyState,
  PanelErrorState,
  PanelSection,
  type PanelError,
} from "./panel-primitives";

/*
 * Escalations queue (issue #123 panel 2): jobs in blocked/escalated states
 * with age (time in state) and owner. Oldest first — the queue leads with
 * whatever has been stuck longest. One masked customer reference per row is
 * the only personal-data-adjacent field on screen (PII-HANDLING.md).
 */

export interface EscalationsPanelProps {
  rows: EscalationRow[] | null;
  error: PanelError | null;
  /** Status pages that failed while the rest succeeded (partial data). */
  failedStatuses: JobStatus[];
  generatedAt: string;
}

const SEVERITY_STYLES: Record<EscalationRow["rule"]["severity"], string> = {
  escalated: "bg-red-50 text-red-800 ring-red-300",
  blocked: "bg-amber-50 text-amber-800 ring-amber-300",
  failed: "bg-slate-100 text-slate-700 ring-slate-300",
};

export function EscalationsPanel(props: EscalationsPanelProps) {
  const { rows, error, failedStatuses, generatedAt } = props;

  return (
    <PanelSection
      id="escalations"
      title="Escalations queue"
      description="Jobs in blocked or escalated states — escalated, awaiting customer approval, or failed — with their age in state and owner. Oldest first."
      status={<span data-testid="escalations-updated">Checked {generatedAt}</span>}
    >
      {error !== null ? (
        <PanelErrorState
          error={error}
          hint="The escalations pages come from the jobs service. Other panels keep working; the queue resumes on the next refresh."
        />
      ) : rows !== null && rows.length === 0 && failedStatuses.length === 0 ? (
        <PanelEmptyState message="No jobs in blocked or escalated states. Quiet board." />
      ) : rows === null ? (
        <PanelEmptyState message="No escalation data available." />
      ) : (
        <>
          {failedStatuses.length > 0 ? (
            <p
              role="status"
              className="mb-3 rounded-md border border-amber-300 bg-amber-50 p-3 text-sm text-amber-900"
            >
              Partial data: the {failedStatuses.join(", ")} page(s) could not be fetched. Rows below
              may be incomplete.
            </p>
          ) : null}
          <div className="console-scroll max-h-96 overflow-y-auto rounded-md border border-slate-200">
            <table className="min-w-full divide-y divide-slate-200 text-sm">
              <caption className="sr-only">
                Escalated jobs with severity, masked references, age in state and last update
              </caption>
              <thead className="sticky top-0 bg-slate-100 text-left text-xs font-semibold uppercase tracking-wide text-slate-600">
                <tr>
                  <th scope="col" className="px-3 py-2">Severity</th>
                  <th scope="col" className="px-3 py-2">Job</th>
                  <th scope="col" className="px-3 py-2">State</th>
                  <th scope="col" className="px-3 py-2">Summary</th>
                  <th scope="col" className="px-3 py-2">Owner</th>
                  <th scope="col" className="px-3 py-2">Customer</th>
                  <th scope="col" className="px-3 py-2">Age in state</th>
                  <th scope="col" className="px-3 py-2">Updated</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-100 bg-white">
                {rows.map((row) => (
                  <tr key={row.job.id} className="hover:bg-slate-50">
                    <td className="px-3 py-2">
                      <span
                        className={`inline-flex items-center rounded-full px-2 py-0.5 text-xs font-semibold ring-1 ring-inset ${SEVERITY_STYLES[row.rule.severity]}`}
                      >
                        {row.rule.severity}
                      </span>
                    </td>
                    <td className="px-3 py-2 font-mono text-xs text-slate-500">
                      {maskIdentifier(row.job.id, "job")}
                    </td>
                    <td className="px-3 py-2 text-slate-800">{row.rule.label}</td>
                    <td className="max-w-72 truncate px-3 py-2 text-slate-800" title={row.job.problem_summary}>
                      {row.job.problem_summary}
                    </td>
                    <td className="px-3 py-2 font-mono text-xs text-slate-500">
                      {maskTechnicianId(row.job.technician_id)}
                    </td>
                    <td className="px-3 py-2 font-mono text-xs text-slate-500">
                      {maskCustomerId(row.job.customer_id)}
                    </td>
                    <td className="px-3 py-2 font-semibold text-slate-800">{formatAge(row.ageMs)}</td>
                    <td className="px-3 py-2 text-xs text-slate-500">
                      {formatTimestamp(row.job.updated_at)}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </>
      )}
    </PanelSection>
  );
}
