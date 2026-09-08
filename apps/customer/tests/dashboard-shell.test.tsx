import { render, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { DashboardShell } from "@/components/dashboard/dashboard-shell";

describe("Dashboard shell", () => {
  it("renders the sidebar navigation with all five areas", () => {
    renderShell();
    const nav = screen.getByRole("navigation", { name: "Dashboard" });
    expect(nav).toBeInTheDocument();

    for (const label of ["Dashboard", "Vehicles", "Jobs", "Passport", "Settings"]) {
      expect(within(nav).getByText(label)).toBeInTheDocument();
    }
  });

  it("links ready areas and marks planned areas as not yet available", () => {
    renderShell();
    const nav = screen.getByRole("navigation", { name: "Dashboard" });

    expect(within(nav).getByRole("link", { name: "Dashboard" })).toHaveAttribute(
      "href",
      "/dashboard",
    );
    expect(within(nav).getByRole("link", { name: "Vehicles" })).toHaveAttribute(
      "href",
      "/dashboard/vehicles",
    );
    // Planned items are disabled entries, not broken links.
    expect(within(nav).queryAllByRole("link", { name: "Jobs" })).toHaveLength(0);
    expect(within(nav).getByText("Jobs").closest("span")).toHaveAttribute("aria-disabled", "true");
    expect(within(nav).getAllByText("soon")).toHaveLength(3);
  });

  it("renders the topbar with the honest API-pending badge", () => {
    renderShell(<p>Panel content</p>);
    expect(screen.getByText(/API integration pending — UI foundation/i)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /Motivra Drive$/i })).toBeInTheDocument();
  });

  it("renders children inside the main landmark", () => {
    renderShell(<p>Panel content</p>);
    const main = screen.getByRole("main");
    expect(main).toBeInTheDocument();
    expect(within(main).getByText("Panel content")).toBeInTheDocument();
  });
});

function renderShell(children?: React.ReactNode) {
  render(<DashboardShell>{children ?? <p>Shell body</p>}</DashboardShell>);
}
