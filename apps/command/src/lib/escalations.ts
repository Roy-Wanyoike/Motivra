/*
 * Escalations queue — jobs in blocked/escalated states, with age and owner.
 *
 * The escalation rules are explicit and operator-reviewable, derived from
 * the 18-status machine (contracts/jobs/openapi.yaml):
 *
 *   ESCALATED         explicitly escalated (state machine or operator move)
 *   AWAITING_APPROVAL blocked on the customer: the estimate cannot proceed
 *                     to repair until APPROVED (ADR-0004 approval gate)
 *   FAILED            terminal failure — needs an operator follow-up
 *                     decision (postmortem / re-request)
 *
 * Age is time-in-state: `updated_at` is stamped by every transition, so
 * now - updated_at is exactly how long the job has been sitting here.
 */

import { fetchJobs, type JobRecord, type JobStatus } from "./api/jobs";

export interface EscalationRule {
  status: JobStatus;
  severity: "escalated" | "blocked" | "failed";
  label: string;
  note: string;
}

export const ESCALATION_RULES: readonly EscalationRule[] = [
  {
    status: "ESCALATED",
    severity: "escalated",
    label: "Escalated",
    note: "Explicitly escalated — an operator must intervene.",
  },
  {
    status: "AWAITING_APPROVAL",
    severity: "blocked",
    label: "Blocked — awaiting customer approval",
    note: "Estimate sent; repair cannot start until the customer approves.",
  },
  {
    status: "FAILED",
    severity: "failed",
    label: "Failed — needs follow-up",
    note: "Terminal failure state; decide postmortem or re-request.",
  },
];

export interface EscalationRow {
  job: JobRecord;
  rule: EscalationRule;
  /** Time in state (now - updated_at), milliseconds. */
  ageMs: number;
}

/** Pure builder — unit-tested core of the escalations panel. */
export function buildEscalations(
  pages: ReadonlyArray<{ status: JobStatus; jobs: JobRecord[] }>,
  now: Date,
): EscalationRow[] {
  const rows: EscalationRow[] = [];
  for (const page of pages) {
    const rule = ESCALATION_RULES.find((candidate) => candidate.status === page.status);
    if (!rule) {
      continue;
    }
    for (const job of page.jobs) {
      const updated = Date.parse(job.updated_at);
      const ageMs = Number.isFinite(updated) ? Math.max(0, now.getTime() - updated) : 0;
      rows.push({ job, rule, ageMs });
    }
  }
  // Oldest first: the queue leads with whatever has been stuck longest.
  rows.sort((a, b) => b.ageMs - a.ageMs);
  return rows;
}

/**
 * Fetch every escalation status page in parallel. Each page degrades
 * independently: a failed page contributes no rows and its failure is
 * reported so the panel can show a partial-data warning.
 */
export async function fetchEscalationPages(
  jobsUrl: string,
  authToken: string,
  timeoutMs = 4000,
): Promise<{ pages: Array<{ status: JobStatus; jobs: JobRecord[] }>; failedStatuses: JobStatus[] }> {
  const settled = await Promise.allSettled(
    ESCALATION_RULES.map(async (rule) => ({
      status: rule.status,
      jobs: await fetchJobs(jobsUrl, { status: rule.status, limit: 200 }, authToken, timeoutMs),
    })),
  );
  const pages: Array<{ status: JobStatus; jobs: JobRecord[] }> = [];
  const failedStatuses: JobStatus[] = [];
  for (const result of settled) {
    if (result.status === "fulfilled") {
      pages.push(result.value);
    } else {
      failedStatuses.push(
        (result.reason as { status?: JobStatus })?.status ?? "ESCALATED",
      );
    }
  }
  return { pages, failedStatuses };
}

/** "2d 4h" / "3h 12m" / "45m" / "just now" — display form of an age. */
export function formatAge(ms: number): string {
  if (!Number.isFinite(ms) || ms <= 0) {
    return "just now";
  }
  const minutes = Math.floor(ms / 60000);
  if (minutes < 1) {
    return "just now";
  }
  const hours = Math.floor(minutes / 60);
  if (hours < 1) {
    return `${minutes}m`;
  }
  const days = Math.floor(hours / 24);
  if (days < 1) {
    return `${hours}h ${minutes % 60}m`;
  }
  return `${days}d ${hours % 24}h`;
}
