/*
 * Jobs read model for the Command job board — shapes and paths verbatim
 * from contracts/jobs/openapi.yaml (listJobs, GET /v1/jobs).
 */

import { upstreamFetch } from "./upstream";

/** The 18 job statuses (contracts/jobs/openapi.yaml JobStatus). */
export const JOB_STATUSES = [
  "CREATED",
  "TRIAGING",
  "DISPATCHING",
  "ASSIGNED",
  "ACCEPTED",
  "EN_ROUTE",
  "ARRIVED",
  "INSPECTION",
  "DIAGNOSIS",
  "ESTIMATE",
  "AWAITING_APPROVAL",
  "APPROVED",
  "REPAIRING",
  "VERIFICATION",
  "COMPLETED",
  "CANCELLED",
  "FAILED",
  "ESCALATED",
] as const;

export type JobStatus = (typeof JOB_STATUSES)[number];

export interface JobRecord {
  id: string;
  service_request_id?: string;
  customer_id: string;
  vehicle_id?: string;
  status: JobStatus;
  problem_summary: string;
  technician_id?: string;
  created_at: string;
  updated_at: string;
}

export interface JobListFilters {
  status?: JobStatus | "ALL";
  technicianId?: string;
  limit?: number;
}

function listPath(filters: JobListFilters): string {
  const params = new URLSearchParams();
  if (filters.status && filters.status !== "ALL") {
    params.set("status", filters.status);
  }
  if (filters.technicianId && filters.technicianId.trim() !== "") {
    params.set("technician_id", filters.technicianId.trim());
  }
  params.set("limit", String(filters.limit ?? 200));
  const query = params.toString();
  return `/v1/jobs${query === "" ? "" : `?${query}`}`;
}

/**
 * GET /v1/jobs — one page of jobs, newest first, optionally narrowed by
 * status and technician (status omitted = every status, the operator board
 * view added for this console).
 */
export async function fetchJobs(
  baseUrl: string,
  filters: JobListFilters,
  authToken: string,
  timeoutMs = 4000,
): Promise<JobRecord[]> {
  const rows = await upstreamFetch<JobRecord[]>(baseUrl, listPath(filters), {
    authToken,
    timeoutMs,
  });
  return Array.isArray(rows) ? rows : [];
}
