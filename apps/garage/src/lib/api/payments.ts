/*
 * Payments (ledger) read model for the Garage invoices panel — shapes and
 * paths verbatim from contracts/payments/openapi.yaml (listIntents,
 * getIntentTransactions). Read-only: this console never initiates payments
 * or refunds (money-mover mutations stay with staff tooling for now).
 *
 * Scope: the payments service derives the data boundary EXCLUSIVELY from
 * the verified token claims (backend/payments/scope.go ScopeFromClaims — a
 * tenant claim bounds an organizational principal to its tenant's rows).
 * This console sends no garage/tenant parameter and needs none: the
 * garage's own identity is the token.
 */

import { upstreamFetch } from "./upstream";

/** Payment intent statuses (contracts/payments/openapi.yaml IntentStatus). */
export const INTENT_STATUSES = [
  "CREATED",
  "PROCESSING",
  "SUCCEEDED",
  "FAILED",
  "CANCELED",
] as const;

export type IntentStatus = (typeof INTENT_STATUSES)[number];

/** Settlement attempt statuses (TransactionStatus). */
export const TRANSACTION_STATUSES = ["PENDING", "SUCCEEDED", "FAILED"] as const;

export type TransactionStatus = (typeof TRANSACTION_STATUSES)[number];

export interface PaymentIntentRecord {
  id: string;
  tenant_id?: string;
  customer_id?: string;
  created_by: string;
  /** Integer KES minor units (cents) — never a float. */
  amount_minor: number;
  currency: string;
  status: IntentStatus;
  provider: string;
  provider_ref?: string;
  job_id?: string;
  vehicle_id?: string;
  description?: string;
  failure_reason?: string;
  succeeded_at?: string;
  failed_at?: string;
  canceled_at?: string;
  created_at: string;
  updated_at: string;
}

export interface PaymentTransactionRecord {
  id: string;
  intent_id: string;
  provider: string;
  provider_ref?: string;
  provider_receipt?: string;
  status: TransactionStatus;
  amount_minor: number;
  currency: string;
  failure_reason?: string;
  occurred_at?: string;
  created_at: string;
  updated_at: string;
}

export interface IntentListFilters {
  status?: IntentStatus;
  jobId?: string;
  limit?: number;
}

function listPath(filters: IntentListFilters): string {
  const params = new URLSearchParams();
  if (filters.status) {
    params.set("status", filters.status);
  }
  if (filters.jobId && filters.jobId.trim() !== "") {
    params.set("job_id", filters.jobId.trim());
  }
  params.set("limit", String(filters.limit ?? 100));
  const query = params.toString();
  return `/v1/payments/intents${query === "" ? "" : `?${query}`}`;
}

/**
 * GET /v1/payments/intents — one page of the garage's ledger intents,
 * scoped server-side by the caller's token claims.
 */
export async function fetchPaymentIntents(
  baseUrl: string,
  filters: IntentListFilters,
  authToken: string,
  timeoutMs = 4000,
): Promise<PaymentIntentRecord[]> {
  const rows = await upstreamFetch<PaymentIntentRecord[]>(baseUrl, listPath(filters), {
    authToken,
    timeoutMs,
  });
  return Array.isArray(rows) ? rows : [];
}

/**
 * GET /v1/payments/intents/{intentID}/transactions — the settlement
 * attempts for one intent (the intent must be inside the caller's scope;
 * an out-of-scope intent reads as 404).
 */
export async function fetchIntentTransactions(
  baseUrl: string,
  intentId: string,
  authToken: string,
  timeoutMs = 4000,
): Promise<PaymentTransactionRecord[]> {
  const rows = await upstreamFetch<PaymentTransactionRecord[]>(
    baseUrl,
    `/v1/payments/intents/${encodeURIComponent(intentId)}/transactions`,
    { authToken, timeoutMs },
  );
  return Array.isArray(rows) ? rows : [];
}

const UUID_PATTERN =
  /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

/** True when `value` is a well-formed UUID (used to vet the ?intent= drill-down). */
export function isUuid(value: string): boolean {
  return UUID_PATTERN.test(value);
}
