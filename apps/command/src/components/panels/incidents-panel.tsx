import { PanelSection } from "./panel-primitives";

/*
 * Incidents panel (issue #123 panel 4) — HONEST PLACEHOLDER.
 *
 * The MVP brief: ship incidents ONLY if a read API already exists. Verified
 * against contracts/ (identity, vehicles, jobs, dispatch, inspections,
 * payments) and every cmd/* service: there is NO incidents read model
 * anywhere in the backend — the only "incident" in the codebase is the
 * unrelated vehicle-history event type. Inventing a table/API would require
 * new migrations, which this MVP forbids. The gap is tracked in the child
 * issue linked from the PR; this card ships so the console shows four
 * panels with the real state instead of silently hiding one.
 */
export const INCIDENTS_TRACKING_ISSUE = "https://github.com/Roy-Wanyoike/Motivra/issues/128";

export function IncidentsPanel() {
  return (
    <PanelSection
      id="incidents"
      title="Incidents"
      description="Operational incidents with severity, status and timeline — deliberately NOT shipped in this MVP."
    >
      <div className="rounded-md border border-slate-200 bg-slate-50 p-4 text-sm leading-relaxed text-slate-600">
        <p className="font-semibold text-slate-800">No incidents read API exists yet.</p>
        <p className="mt-2">
          The backend (contracts/ and every cmd/* service) has no incidents read model, and this
          MVP does not invent APIs or migrations. Until the incidents read model lands, incident
          response runs through the observability stack and the RUNBOOK incident procedure
          (docs/RUNBOOK.md §9).
        </p>
        <p className="mt-2">
          Tracked in{" "}
          <a
            className="font-semibold text-amber-800 underline underline-offset-4"
            href={INCIDENTS_TRACKING_ISSUE}
            rel="noreferrer"
          >
            issue #128 — incidents read model
          </a>
          . This panel will render live data as soon as that API ships.
        </p>
      </div>
    </PanelSection>
  );
}
