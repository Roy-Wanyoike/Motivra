/*
 * System health render test (issue #123): every cmd/* service renders with
 * its green/amber/red verdict, the summary counts non-green services, and a
 * not-ready service exposes failed checker NAMES only (never error detail).
 */

import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { SystemHealthPanel } from "@/components/panels/system-health-panel";
import { classifyServiceHealth, HEALTH_SERVICES, type ServiceHealth } from "@/lib/api/health";
import type { PanelError } from "@/components/panels/panel-primitives";

const service = (name: ServiceHealth["service"], state: ServiceHealth["state"]): ServiceHealth => ({
  service: name,
  state,
  healthz:
    state === "red"
      ? { outcome: "unreachable", latencyMs: 4000 }
      : { outcome: "ok", latencyMs: 3 },
  readyz:
    state === "green"
      ? { outcome: "ok", latencyMs: 5 }
      : state === "amber"
        ? { outcome: "not-ready", failed: ["postgres"], latencyMs: 7 }
        : { outcome: "unreachable", latencyMs: 4000 },
});

describe("classifyServiceHealth — the green/amber/red verdict", () => {
  it("classifies live+ready green, live-but-not-ready amber, everything else red", () => {
    expect(classifyServiceHealth({ outcome: "ok" }, { outcome: "ok" })).toBe("green");
    expect(classifyServiceHealth({ outcome: "ok" }, { outcome: "not-ready" })).toBe("amber");
    expect(classifyServiceHealth({ outcome: "unreachable" }, { outcome: "ok" })).toBe("red");
    expect(classifyServiceHealth({ outcome: "unreachable" }, { outcome: "unreachable" })).toBe("red");
    expect(classifyServiceHealth({ outcome: "ok" }, { outcome: "unreachable" })).toBe("red");
  });

  it("covers all six Wave 1-5 services", () => {
    expect([...HEALTH_SERVICES].sort()).toEqual([
      "dispatch",
      "identity",
      "inspections",
      "jobs",
      "payments",
      "vehicles",
    ]);
  });
});

describe("SystemHealthPanel", () => {
  // Every service green except jobs, which is degraded (live but not ready):
  // the service-down reflection the panel must surface.
  const services = HEALTH_SERVICES.map((name) => service(name, name === "jobs" ? "amber" : "green"));

  it("renders one card per service with verdicts and the green summary", () => {
    render(<SystemHealthPanel services={services} error={null} checkedAt="2026-10-21 12:00:00 UTC" />);

    expect(screen.getByRole("region", { name: /system health/i })).toBeInTheDocument();
    for (const name of HEALTH_SERVICES) {
      expect(screen.getByTestId(`health-${name}`)).toBeInTheDocument();
    }
    expect(screen.getByTestId("health-summary")).toHaveTextContent("5/6 green");
    expect(screen.getByTestId("health-jobs")).toHaveTextContent("failed checks: postgres");
    expect(screen.getByTestId("health-jobs").textContent).toContain("readiness not-ready");
    // Checker NAMES only — the platform never exposes dependency error detail.
    expect(screen.getByTestId("health-jobs").textContent).not.toContain("connection refused");
  });

  it("marks an unreachable service red with no readiness detail", () => {
    render(
      <SystemHealthPanel
        services={[service("jobs", "red"), ...HEALTH_SERVICES.filter((s) => s !== "jobs").map((s) => service(s, "green"))]}
        error={null}
        checkedAt="x"
      />,
    );
    expect(screen.getByTestId("health-summary")).toHaveTextContent("5/6 green");
    expect(screen.getByTestId("health-jobs").textContent).toContain("liveness unreachable");
  });

  it("degrades to a panel-scoped error state when the probes themselves failed", () => {
    const error: PanelError = { code: "unexpected_error", title: "The panel failed to load", unreachable: false };
    render(<SystemHealthPanel services={null} error={error} checkedAt="x" />);
    expect(screen.getByRole("status")).toHaveTextContent("this panel is degraded");
    expect(screen.queryByTestId("health-jobs")).not.toBeInTheDocument();
  });
});
