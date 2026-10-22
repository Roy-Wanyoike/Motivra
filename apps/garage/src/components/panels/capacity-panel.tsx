import { PanelSection, PendingCapabilityState } from "./panel-primitives";
import { CAPACITY_TRACKING_ISSUE, CAPACITY_TRACKING_LABEL } from "./trackers";

/*
 * Capacity configuration panel (issue #124 panel 3) — HONEST PLACEHOLDER.
 *
 * No capacity concept exists in the backend: audited contracts/ and every
 * cmd/* / backend/* domain — there is no bays/hours/technician-slots
 * endpoint, table or migration. Inventing one would require new APIs and
 * migrations, which this MVP forbids; the panel ships as a pending-
 * capability card describing the intended surface (the Command MVP pattern,
 * issue #123's incidents panel).
 */
export function CapacityPanel() {
  return (
    <PanelSection
      id="capacity"
      title="Capacity"
      description="Declare this garage's service capacity — bays, operating hours and technician slots — so Motivra scheduling and dispatch can respect it."
    >
      <PendingCapabilityState
        capability="the capacity configuration API"
        trackingIssue={CAPACITY_TRACKING_ISSUE}
        trackingLabel={CAPACITY_TRACKING_LABEL}
        detail="The backend has no capacity endpoints, storage or contracts yet (verified across contracts/ and every cmd/* service during the issue #124 API gap check). This console does not invent APIs, so capacity configuration ships disabled rather than editing values that would not persist."
      />
      <div className="mt-4 rounded-md border border-slate-200 p-3">
        <h3 className="text-xs font-semibold uppercase tracking-wide text-slate-600">
          The configuration surface this panel will offer (not yet wired)
        </h3>
        <ul className="mt-2 list-disc space-y-1 pl-5 text-sm text-slate-600">
          <li>
            <span className="font-semibold text-slate-800">Bays</span> — service lanes available
            for booked work.
          </li>
          <li>
            <span className="font-semibold text-slate-800">Operating hours</span> — opening and
            closing time for appointment scheduling.
          </li>
          <li>
            <span className="font-semibold text-slate-800">Technician slots</span> — parallel
            technician capacity per slot.
          </li>
        </ul>
        <p className="mt-3 text-xs leading-relaxed text-slate-500">
          All values will be read and written server-side with your session token; the backend
          derives the garage from your sign-in (the tenant claim) — nothing here is typed in or
          trusted from the browser.
        </p>
      </div>
    </PanelSection>
  );
}
