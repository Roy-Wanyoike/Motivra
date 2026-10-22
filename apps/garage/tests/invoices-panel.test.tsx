/*
 * Invoices panel render tests (issue #124 panel 4 — the LIVE ledger view).
 *
 * Proven: rows render the payments intents (the ledger), money renders
 * from integer KES minor units, identifier references are masked by
 * default (raw UUIDs never reach the DOM), the drill-down link is present,
 * and degraded backends degrade ONLY the panel (error state — no crash,
 * no rows, no fake data).
 */

import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { InvoicesPanel } from "@/components/panels/invoices-panel";
import type { PaymentIntentRecord, PaymentTransactionRecord } from "@/lib/api/payments";
import type { PanelError } from "@/components/panels/panel-primitives";
import { formatKes } from "@/lib/format";

const INTENT_ID = "d1212121-3434-4555-8676-787898990001";
const JOB_ID = "c9999999-8888-4777-8666-555544443333";
const CUSTOMER_ID = "a1111111-2222-4333-8444-555566667777";

const intent: PaymentIntentRecord = {
  id: INTENT_ID,
  customer_id: CUSTOMER_ID,
  created_by: "b5555555-6666-4777-8888-999900001111",
  amount_minor: 123456,
  currency: "KES",
  status: "SUCCEEDED",
  provider: "sandbox",
  job_id: JOB_ID,
  created_at: "2026-10-20T08:30:00Z",
  updated_at: "2026-10-21T09:14:00Z",
  succeeded_at: "2026-10-21T09:14:00Z",
};

const transactions: PaymentTransactionRecord[] = [
  {
    id: "e5555555-4444-4333-8222-110000009999",
    intent_id: INTENT_ID,
    provider: "sandbox",
    provider_receipt: "SBXR-7QK4T2",
    status: "SUCCEEDED",
    amount_minor: 123456,
    currency: "KES",
    occurred_at: "2026-10-21T09:14:00Z",
    created_at: "2026-10-20T08:30:00Z",
    updated_at: "2026-10-21T09:14:00Z",
  },
];

function rowFor(text: string): HTMLElement {
  const cell = screen.getByText(text);
  expect(cell.tagName).toBe("TD");
  return cell.closest("tr") as HTMLElement;
}

describe("InvoicesPanel — the live ledger", () => {
  it("renders ledger rows with KES amounts from integer minor units and masked references", () => {
    render(
      <InvoicesPanel
        intents={[intent]}
        error={null}
        selectedIntentId={null}
        transactions={null}
        transactionsError={null}
        generatedAt="2026-10-21 12:00:00 UTC"
      />,
    );

    expect(screen.getByRole("region", { name: /invoices/i })).toBeInTheDocument();
    expect(screen.getByText("KES 1,234.56")).toBeInTheDocument();
    expect(screen.getByTestId("invoices-updated")).toHaveTextContent(
      "Data as of 2026-10-21 12:00:00 UTC",
    );

    const row = rowFor("intent_····0001");
    expect(row.textContent).toContain("succeeded");
    expect(row.textContent).toContain("job_····3333");
    expect(row.textContent).toContain("customer_····7777");
    expect(row.textContent).toContain("sandbox");

    // Masking is the DEFAULT: no raw identifier survives into the DOM.
    expect(screen.queryByText(INTENT_ID)).not.toBeInTheDocument();
    expect(screen.queryByText(JOB_ID)).not.toBeInTheDocument();
    expect(screen.queryByText(CUSTOMER_ID)).not.toBeInTheDocument();
  });

  it("renders failed and processing entries with their provider verdicts", () => {
    render(
      <InvoicesPanel
        intents={[
          { ...intent, id: INTENT_ID, status: "FAILED", failure_reason: "payer cancelled" },
          { ...intent, id: "d1212121-3434-4555-8676-787898990002", status: "PROCESSING" },
        ]}
        error={null}
        selectedIntentId={null}
        transactions={null}
        transactionsError={null}
        generatedAt="x"
      />,
    );
    expect(screen.getByText("failed")).toBeInTheDocument();
    expect(screen.getByText("processing")).toBeInTheDocument();
  });

  it("shows the transactions drill-down link for every entry", () => {
    render(
      <InvoicesPanel
        intents={[intent]}
        error={null}
        selectedIntentId={null}
        transactions={null}
        transactionsError={null}
        generatedAt="x"
      />,
    );
    const link = screen.getByRole("link", { name: "Transactions" });
    expect(link).toHaveAttribute("href", `/?intent=${encodeURIComponent(INTENT_ID)}#invoices`);
  });

  it("renders the selected intent's settlement attempts (masked attempt ids, receipt shown)", () => {
    render(
      <InvoicesPanel
        intents={[intent]}
        error={null}
        selectedIntentId={INTENT_ID}
        transactions={transactions}
        transactionsError={null}
        generatedAt="x"
      />,
    );
    expect(screen.getByText(/Settlement attempts for intent_····0001/)).toBeInTheDocument();
    expect(screen.getByText("SBXR-7QK4T2")).toBeInTheDocument();
    expect(screen.queryByText(transactions[0].id)).not.toBeInTheDocument();
  });

  it("shows an empty state (not a table) when the garage has no ledger entries", () => {
    render(
      <InvoicesPanel
        intents={[]}
        error={null}
        selectedIntentId={null}
        transactions={null}
        transactionsError={null}
        generatedAt="x"
      />,
    );
    expect(screen.getByText(/No ledger entries yet/)).toBeInTheDocument();
    expect(screen.queryByRole("table")).not.toBeInTheDocument();
  });
});

describe("InvoicesPanel — degraded states (no crashes, no fake data)", () => {
  it("degrades to a panel-scoped error state when the payments service is unreachable", () => {
    const error: PanelError = {
      code: "upstream_unreachable",
      title: "The service did not respond",
      unreachable: true,
      status: 0,
    };
    render(
      <InvoicesPanel
        intents={null}
        error={error}
        selectedIntentId={null}
        transactions={null}
        transactionsError={null}
        generatedAt="x"
      />,
    );
    expect(screen.getByRole("status")).toHaveTextContent("this panel is degraded");
    expect(screen.getByText(/upstream_unreachable/)).toBeInTheDocument();
    expect(screen.queryByRole("table")).not.toBeInTheDocument();
  });

  it("degrades explicitly on 401/403 (server answered, caller not authorized)", () => {
    const error: PanelError = {
      code: "upstream_403",
      title: "The service answered HTTP 403",
      unreachable: false,
      status: 403,
    };
    render(
      <InvoicesPanel
        intents={null}
        error={error}
        selectedIntentId={null}
        transactions={null}
        transactionsError={null}
        generatedAt="x"
      />,
    );
    expect(screen.getByRole("status")).toHaveTextContent("this panel is degraded");
    expect(screen.getByText(/upstream_403/)).toBeInTheDocument();
    expect(screen.queryByRole("table")).not.toBeInTheDocument();
  });

  it("keeps the ledger usable when only the drill-down transactions call fails", () => {
    render(
      <InvoicesPanel
        intents={[intent]}
        error={null}
        selectedIntentId={INTENT_ID}
        transactions={null}
        transactionsError={{
          code: "upstream_503",
          title: "The service answered HTTP 503",
          unreachable: false,
          status: 503,
        }}
        generatedAt="x"
      />,
    );
    expect(screen.getByText(/Transactions unavailable right now/)).toBeInTheDocument();
    expect(screen.getByRole("table")).toBeInTheDocument(); // the ledger itself
  });
});

describe("formatKes — money discipline at the display boundary", () => {
  it("renders integer minor units without floats ever touching the value", () => {
    expect(formatKes(123456)).toBe("KES 1,234.56");
    expect(formatKes(5)).toBe("KES 0.05");
    expect(formatKes(100)).toBe("KES 1.00");
    expect(formatKes(123456789)).toBe("KES 1,234,567.89");
  });

  it("refuses non-integer and negative amounts rather than guessing", () => {
    expect(formatKes(12.5)).toBe("—");
    expect(formatKes(-1)).toBe("—");
    expect(formatKes(Number.NaN)).toBe("—");
  });
});
