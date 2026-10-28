import type { JobStatus } from "@/lib/api/jobs";

/*
 * Status badge for the 18 job statuses. Palette discipline: slate for
 * early states, teal for in-progress work, amber for blocked, red for
 * escalated/failed, emerald for completed — no blues.
 */

const STATUS_STYLES: Record<JobStatus, string> = {
  CREATED: "bg-slate-100 text-slate-700 ring-slate-300",
  TRIAGING: "bg-slate-100 text-slate-700 ring-slate-300",
  DISPATCHING: "bg-slate-100 text-slate-700 ring-slate-300",
  ASSIGNED: "bg-teal-50 text-teal-800 ring-teal-300",
  ACCEPTED: "bg-teal-50 text-teal-800 ring-teal-300",
  EN_ROUTE: "bg-teal-50 text-teal-800 ring-teal-300",
  ARRIVED: "bg-teal-50 text-teal-800 ring-teal-300",
  INSPECTION: "bg-teal-50 text-teal-800 ring-teal-300",
  DIAGNOSIS: "bg-teal-50 text-teal-800 ring-teal-300",
  ESTIMATE: "bg-teal-50 text-teal-800 ring-teal-300",
  AWAITING_APPROVAL: "bg-amber-50 text-amber-800 ring-amber-300",
  APPROVED: "bg-teal-50 text-teal-800 ring-teal-300",
  REPAIRING: "bg-teal-50 text-teal-800 ring-teal-300",
  VERIFICATION: "bg-teal-50 text-teal-800 ring-teal-300",
  COMPLETED: "bg-emerald-50 text-emerald-800 ring-emerald-300",
  CANCELLED: "bg-slate-100 text-slate-500 ring-slate-200",
  FAILED: "bg-red-50 text-red-800 ring-red-300",
  ESCALATED: "bg-red-50 text-red-800 ring-red-300",
};

export function StatusBadge({ status }: { status: JobStatus }) {
  return (
    <span
      className={`inline-flex items-center rounded-full px-2 py-0.5 text-xs font-semibold ring-1 ring-inset ${STATUS_STYLES[status] ?? "bg-slate-100 text-slate-700 ring-slate-300"}`}
    >
      {status.replace(/_/g, " ")}
    </span>
  );
}
