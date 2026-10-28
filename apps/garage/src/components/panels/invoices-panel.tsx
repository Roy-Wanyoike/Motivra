import Link from "next/link";
import {
  type PaymentIntentRecord,
  type PaymentTransactionRecord,
  type IntentStatus,
} from "@/lib/api/payments";
import { maskCustomerId, maskIdentifier, maskIntentId } from "@/lib/pii/mask";
import { formatKes, formatTimestamp } from "@/lib/format";
import {
  PanelEmptyState,
  PanelErrorState,
  PanelSection,
  type PanelError,
} from "./panel-primitives";

/*
 * Invoices panel (issue #124 panel 4) — LIVE, read-only.
 *
 * The garage's ledger as the payments service holds it: payment intents
 * (GET /v1/payments/intents) and, on drill-down, one intent's settlement
 * attempts (GET /v1/payments/intents/{id}/transactions) — endpoints and
 * shapes verbatim from contracts/payments/openapi.yaml. The data boundary
 * is the caller's own token: the payments service scopes reads to the
 * token's tenant claim (backend/payments/scope.go ScopeFromClaims), so a
 * garage staff member sees exactly their garage's ledger entries and this
 * panel never sends a garage identifier. Amounts render from integer KES
 * minor units (never floats).
 *
 * MVP honesty: a dedicated "invoice document" aggregate does not exist in
 * the payments domain — this view IS the ledger (intents + transactions),
 * which is what issue #124's acceptance criterion ("invoice list matches
 * ledger entries for the garage") specifies.
 */

export interface InvoicesPanelProps {
  intents: PaymentIntentRecord[] | null;
  error: PanelError | null;
  selectedIntentId: string | null;
  transactions: PaymentTransactionRecord[] | null;
  transactionsError: PanelError | null;
  generatedAt: string;
}

const INTENT_STYLES: Record<IntentStatus, string> = {
  CREATED: "bg-slate-100 text-slate-700 ring-slate-300",
  PROCESSING: "bg-teal-50 text-teal-800 ring-teal-300",
  SUCCEEDED: "bg-emerald-50 text-emerald-800 ring-emerald-300",
  FAILED: "bg-red-50 text-red-800 ring-red-300",
  CANCELED: "bg-slate-100 text-slate-500 ring-slate-200",
};

function IntentBadge({ status }: { status: IntentStatus }) {
  return (
    <span
      className={`inline-flex items-center rounded-full px-2 py-0.5 text-xs font-semibold ring-1 ring-inset ${INTENT_STYLES[status] ?? "bg-slate-100 text-slate-700 ring-slate-300"}`}
    >
      {status.toLowerCase()}
    </span>
  );
}

export function InvoicesPanel(props: InvoicesPanelProps) {
  const { intents, error, selectedIntentId, transactions, transactionsError, generatedAt } = props;

  return (
    <PanelSection
      id="invoices"
      title="Invoices (ledger)"
      description="Read-only view of this garage's payment ledger — intents and their settlement attempts, straight from the payments service. Amounts are KES minor units."
      status={<span data-testid="invoices-updated">Data as of {generatedAt}</span>}
    >
      <div>
        {error !== null ? (
          <PanelErrorState
            error={error}
            hint="The payments service may be down or restarting. Other panels keep working; the ledger resumes on the next refresh."
          />
        ) : intents === null || intents.length === 0 ? (
          <PanelEmptyState message="No ledger entries yet for this garage. Entries appear when payments are initiated against your jobs." />
        ) : (
          <div className="console-scroll max-h-96 overflow-y-auto rounded-md border border-slate-200">
            <table className="min-w-full divide-y divide-slate-200 text-sm">
              <caption className="sr-only">
                Payment ledger entries with amount, status, masked job and customer references and
                timestamps
              </caption>
              <thead className="sticky top-0 bg-slate-100 text-left text-xs font-semibold uppercase tracking-wide text-slate-600">
                <tr>
                  <th scope="col" className="px-3 py-2">Entry</th>
                  <th scope="col" className="px-3 py-2">Status</th>
                  <th scope="col" className="px-3 py-2">Amount</th>
                  <th scope="col" className="px-3 py-2">Provider</th>
                  <th scope="col" className="px-3 py-2">Job</th>
                  <th scope="col" className="px-3 py-2">Customer</th>
                  <th scope="col" className="px-3 py-2">Created</th>
                  <th scope="col" className="px-3 py-2">Settled</th>
                  <th scope="col" className="px-3 py-2"><span className="sr-only">Transactions</span></th>
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-100 bg-white">
                {intents.map((intent) => (
                  <tr key={intent.id} className="hover:bg-slate-50">
                    <td className="px-3 py-2 font-mono text-xs text-slate-500">
                      {maskIntentId(intent.id)}
                    </td>
                    <td className="px-3 py-2">
                      <IntentBadge status={intent.status} />
                    </td>
                    <td className="px-3 py-2 font-medium text-slate-800">
                      {formatKes(intent.amount_minor)}
                    </td>
                    <td className="px-3 py-2 text-xs text-slate-500">{intent.provider}</td>
                    <td className="px-3 py-2 font-mono text-xs text-slate-500">
                      {maskIdentifier(intent.job_id, "job")}
                    </td>
                    <td className="px-3 py-2 font-mono text-xs text-slate-500">
                      {maskCustomerId(intent.customer_id)}
                    </td>
                    <td className="px-3 py-2 text-xs text-slate-500">
                      {formatTimestamp(intent.created_at)}
                    </td>
                    <td className="px-3 py-2 text-xs text-slate-500">
                      {intent.succeeded_at ? formatTimestamp(intent.succeeded_at) : "—"}
                    </td>
                    <td className="px-3 py-2 text-right">
                      <Link
                        href={`/?intent=${encodeURIComponent(intent.id)}#invoices`}
                        className="text-xs font-semibold text-amber-800 underline underline-offset-4"
                      >
                        {selectedIntentId === intent.id ? "Hide transactions" : "Transactions"}
                      </Link>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>

      {selectedIntentId !== null ? (
        <div className="mt-4 rounded-md border border-slate-200 p-3">
          <h3 className="text-xs font-semibold uppercase tracking-wide text-slate-600">
            Settlement attempts for {maskIntentId(selectedIntentId)}
          </h3>
          {transactionsError !== null ? (
            <p className="mt-2 text-sm text-amber-900">
              Transactions unavailable right now ({transactionsError.code}) — the ledger list is
              unaffected.
            </p>
          ) : transactions === null || transactions.length === 0 ? (
            <p className="mt-2 text-sm text-slate-500">
              No settlement attempts recorded for this entry.
            </p>
          ) : (
            <table className="mt-2 min-w-full divide-y divide-slate-200 text-sm">
              <caption className="sr-only">Settlement attempts for the selected ledger entry</caption>
              <thead className="bg-slate-50 text-left text-xs font-semibold uppercase tracking-wide text-slate-600">
                <tr>
                  <th scope="col" className="px-3 py-2">Attempt</th>
                  <th scope="col" className="px-3 py-2">Status</th>
                  <th scope="col" className="px-3 py-2">Amount</th>
                  <th scope="col" className="px-3 py-2">Receipt</th>
                  <th scope="col" className="px-3 py-2">Occurred</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-100 bg-white">
                {transactions.map((tx) => (
                  <tr key={tx.id}>
                    <td className="px-3 py-2 font-mono text-xs text-slate-500">
                      {maskIdentifier(tx.id, "tx")}
                    </td>
                    <td className="px-3 py-2 text-xs font-semibold text-slate-700">
                      {tx.status.toLowerCase()}
                    </td>
                    <td className="px-3 py-2 text-xs text-slate-700">{formatKes(tx.amount_minor)}</td>
                    <td className="px-3 py-2 font-mono text-xs text-slate-500">
                      {tx.provider_receipt ? tx.provider_receipt : "—"}
                    </td>
                    <td className="px-3 py-2 text-xs text-slate-500">
                      {tx.occurred_at ? formatTimestamp(tx.occurred_at) : "—"}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      ) : null}
    </PanelSection>
  );
}
