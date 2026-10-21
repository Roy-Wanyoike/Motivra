/*
 * Escalations queue render test (issue #123): escalated/blocked/failed rows
 * render severity, age and a MASKED customer reference; partial page
 * failures surface a partial-data warning; a dead jobs service degrades to
 * the panel error state.
 */

import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { EscalationsPanel } from "@/components/panels/escalations-panel";
import { buildEscalations, type EscalationRow } from "@/lib/escalations";
import type { JobRecord } from "@/lib/api/jobs";
import type { PanelError } from "@/components/panels/panel-primitives";

const job = (overrides: Partial<JobRecord>): JobRecord => ({
  id: "c1212121-0000-4000-8000-0000000000aa",
  customer_id: "a1111111-2222-4333-8444-555566667777",
  status: "ESCALATED",
  problem_summary: "Technician no-show reported by customer",
  created_at: "2026-10-19T08:00:00Z",
  updated_at: "2026-10-20T08:00:00Z",
  ...overrides,
});

const now = new Date("2026-10-21T12:00:00Z");

function rowsFrom(jobs: JobRecord[]): EscalationRow[] {
  return buildEscalations([{ status: "ESCALATED", jobs }], now);
}

describe("EscalationsPanel", () => {
  it("renders escalation rows with severity, age in state and masked customer", () => {
    render(
      <EscalationsPanel
        rows={rowsFrom([job({ id: "c1212121-0000-4000-8000-0000000000aa" })])}
        error={null}
        failedStatuses={[]}
        generatedAt="2026-10-21 12:00:00 UTC"
      />,
    );

    expect(screen.getByText("escalated")).toBeInTheDocument();
    expect(screen.getByText("Technician no-show reported by customer")).toBeInTheDocument();
    expect(screen.getByText("customer_····7777")).toBeInTheDocument();
    expect(screen.queryByText("a1111111-2222-4333-8444-555566667777")).not.toBeInTheDocument();
    // 2026-10-20T08:00 → 2026-10-21T12:00 = 1d 4h
    expect(screen.getByText("1d 4h")).toBeInTheDocument();
    expect(screen.getByTestId("escalations-updated")).toBeInTheDocument();
  });

  it("warns on partial data when some escalation status pages failed", () => {
    render(
      <EscalationsPanel
        rows={rowsFrom([job({})])}
        error={null}
        failedStatuses={["AWAITING_APPROVAL"]}
        generatedAt="x"
      />,
    );
    expect(screen.getByRole("status")).toHaveTextContent(/Partial data.*AWAITING_APPROVAL/);
  });

  it("shows the quiet-board empty state when nothing is blocked or escalated", () => {
    render(
      <EscalationsPanel rows={[]} error={null} failedStatuses={[]} generatedAt="x" />,
    );
    expect(screen.getByText("No jobs in blocked or escalated states. Quiet board.")).toBeInTheDocument();
  });

  it("degrades to a panel-scoped error state when the jobs service is down", () => {
    const error: PanelError = { code: "upstream_503", title: "The service answered HTTP 503", unreachable: false };
    render(
      <EscalationsPanel rows={null} error={error} failedStatuses={[]} generatedAt="x" />,
    );
    expect(screen.getByRole("status")).toHaveTextContent("this panel is degraded");
    expect(screen.queryByRole("table")).not.toBeInTheDocument();
  });
});
