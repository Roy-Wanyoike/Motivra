/*
 * Garage job board render tests (issue #124 panel 1).
 *
 * The board is wired to the EXISTING GET /v1/jobs; today that endpoint
 * answers 403 for the GARAGE role (operator-only RBAC — the backend child
 * issue tracks the garage-scoped read). Proven here: live rows render
 * masked references when the API ever answers 200 for a garage session,
 * the 403 RBAC gap renders the honest "backend capability pending" state
 * (not a crash, not fake data), and other upstream failures degrade the
 * panel only.
 */

import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { JobBoardPanel, type JobBoardFilters } from "@/components/panels/job-board-panel";
import type { JobRecord } from "@/lib/api/jobs";
import type { PanelError } from "@/components/panels/panel-primitives";

const CUSTOMER_ID = "a1111111-2222-4333-8444-555566667777";
const TECHNICIAN_ID = "b9999999-8888-4777-8666-555544443333";
const JOB_ID = "c1212121-3434-4555-8676-787898990001";

const job: JobRecord = {
  id: JOB_ID,
  customer_id: CUSTOMER_ID,
  vehicle_id: "d1414141-5151-4676-8898-990011112222",
  status: "AWAITING_APPROVAL",
  problem_summary: "Alternator not charging; battery drains overnight",
  technician_id: TECHNICIAN_ID,
  created_at: "2026-10-20T08:30:00Z",
  updated_at: "2026-10-21T09:14:00Z",
};

const filters: JobBoardFilters = { status: "ALL", technician: "" };

function rowFor(id: string): HTMLElement {
  const cell = screen.getByText(id);
  expect(cell.tagName).toBe("TD");
  return cell.closest("tr") as HTMLElement;
}

describe("JobBoardPanel", () => {
  it("renders job rows with masked customer/technician references and no raw UUIDs", () => {
    render(
      <JobBoardPanel
        jobs={[job]}
        error={null}
        filters={filters}
        generatedAt="2026-10-21 12:00:00 UTC"
      />,
    );

    expect(screen.getByRole("region", { name: /job board/i })).toBeInTheDocument();
    expect(screen.getByText("Alternator not charging; battery drains overnight")).toBeInTheDocument();
    expect(screen.getByTestId("board-updated")).toHaveTextContent("Data as of 2026-10-21 12:00:00 UTC");

    const row = rowFor("job_····0001");
    expect(row.textContent).toContain("customer_····7777");
    expect(row.textContent).toContain("technician_····3333");
    // Masking is the DEFAULT: no raw identifier survives into the DOM.
    expect(screen.queryByText(CUSTOMER_ID)).not.toBeInTheDocument();
    expect(screen.queryByText(TECHNICIAN_ID)).not.toBeInTheDocument();
    expect(screen.queryByText(JOB_ID)).not.toBeInTheDocument();
  });

  it("offers the status/technician filter form with all 18 statuses", () => {
    render(
      <JobBoardPanel jobs={[]} error={null} filters={filters} generatedAt="x" />,
    );
    const statusSelect = screen.getByLabelText("Status") as HTMLSelectElement;
    expect(statusSelect.options).toHaveLength(19); // ALL + the 18 statuses
    expect(screen.getByLabelText("Technician ID")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Apply filters" })).toBeInTheDocument();
  });

  it("shows an empty state (not a table) when the filtered page has no jobs", () => {
    render(
      <JobBoardPanel
        jobs={[]}
        error={null}
        filters={{ status: "ESCALATED", technician: "" }}
        generatedAt="x"
      />,
    );
    expect(screen.getByText("No jobs match the current filters.")).toBeInTheDocument();
    expect(screen.queryByRole("table")).not.toBeInTheDocument();
  });
});

describe("JobBoardPanel — degraded states (no crashes, no fake data)", () => {
  it("renders the honest RBAC-gap state when the jobs API answers 403 for the garage role", () => {
    const error: PanelError = {
      code: "upstream_403",
      title: "The service answered HTTP 403",
      unreachable: false,
      status: 403,
    };
    render(
      <JobBoardPanel jobs={null} error={error} filters={filters} generatedAt="x" />,
    );
    const status = screen.getByRole("status");
    expect(status).toHaveTextContent("Backend capability pending");
    expect(status).toHaveTextContent("does not yet admit the GARAGE role");
    expect(status).toHaveTextContent("issue #165");
    expect(screen.queryByRole("table")).not.toBeInTheDocument();
  });

  it("degrades to a panel-scoped error state when the jobs service is unreachable", () => {
    const error: PanelError = {
      code: "upstream_unreachable",
      title: "The service did not respond",
      unreachable: true,
      status: 0,
    };
    render(
      <JobBoardPanel jobs={null} error={error} filters={filters} generatedAt="x" />,
    );
    expect(screen.getByRole("status")).toHaveTextContent("this panel is degraded");
    expect(screen.getByText(/upstream_unreachable/)).toBeInTheDocument();
    expect(screen.queryByRole("table")).not.toBeInTheDocument();
  });
});
