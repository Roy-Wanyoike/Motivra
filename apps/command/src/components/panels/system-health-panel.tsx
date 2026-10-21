import type { ServiceHealth } from "@/lib/api/health";
import {
  PanelEmptyState,
  PanelErrorState,
  PanelSection,
  type PanelError,
} from "./panel-primitives";

/*
 * System health (issue #123 panel 3): aggregates /healthz + /readyz for the
 * six Wave 1-5 Go services with a green/amber/red verdict per service.
 * green = live + ready; amber = live but not ready (degraded); red = not
 * live or unreachable. One dead service never breaks the panel.
 */

export interface SystemHealthPanelProps {
  services: ServiceHealth[] | null;
  error: PanelError | null;
  checkedAt: string;
}

const STATE_DOT: Record<ServiceHealth["state"], { className: string; label: string }> = {
  green: { className: "bg-emerald-500", label: "Green — live and ready" },
  amber: { className: "bg-amber-500", label: "Amber — live but not ready" },
  red: { className: "bg-red-500", label: "Red — not live or unreachable" },
};

export function SystemHealthPanel(props: SystemHealthPanelProps) {
  const { services, error, checkedAt } = props;
  const degraded = services?.filter((service) => service.state !== "green").length ?? 0;

  return (
    <PanelSection
      id="system-health"
      title="System health"
      description="Liveness (/healthz) and readiness (/readyz) for every cmd/* service, probed fresh on each console render."
      status={
        services === null ? null : (
          <span data-testid="health-summary">
            {services.length - degraded}/{services.length} green
          </span>
        )
      }
    >
      {error !== null ? (
        <PanelErrorState error={error} hint="Health probes failed to run at all — check the console deployment environment." />
      ) : services === null ? (
        <PanelEmptyState message="No health data available." />
      ) : (
        <>
          <ul className="grid grid-cols-1 gap-2 sm:grid-cols-2 lg:grid-cols-3" aria-label="Per-service health">
            {services.map((service) => {
              const dot = STATE_DOT[service.state];
              return (
                <li
                  key={service.service}
                  data-testid={`health-${service.service}`}
                  className="flex items-start gap-3 rounded-md border border-slate-200 p-3"
                >
                  <span
                    aria-hidden="true"
                    className={`mt-1 inline-block h-3 w-3 shrink-0 rounded-full ${dot.className}`}
                  />
                  <div className="min-w-0">
                    <p className="text-sm font-semibold text-slate-900">
                      {service.service}
                      <span className="sr-only"> — {dot.label}</span>
                    </p>
                    <p className="text-xs text-slate-500">
                      liveness {service.healthz.outcome} ({service.healthz.latencyMs} ms) · readiness{" "}
                      {service.readyz.outcome} ({service.readyz.latencyMs} ms)
                    </p>
                    {service.readyz.failed !== undefined && service.readyz.failed.length > 0 ? (
                      <p className="mt-1 text-xs font-medium text-amber-800">
                        failed checks: {service.readyz.failed.join(", ")}
                      </p>
                    ) : null}
                  </div>
                </li>
              );
            })}
          </ul>
          <p className="mt-3 text-xs leading-relaxed text-slate-500">
            Legend: <span className="font-semibold text-emerald-700">green</span> live + ready ·{" "}
            <span className="font-semibold text-amber-700">amber</span> live but not ready ·{" "}
            <span className="font-semibold text-red-700">red</span> not live or unreachable. Last
            checked {checkedAt}. Readiness reports failed checker names only — the platform never
            exposes dependency error details on /readyz (hardening #81).
          </p>
        </>
      )}
    </PanelSection>
  );
}
