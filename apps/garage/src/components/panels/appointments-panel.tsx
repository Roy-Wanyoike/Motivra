import { PanelSection, PendingCapabilityState } from "./panel-primitives";
import {
  APPOINTMENTS_TRACKING_ISSUE,
  APPOINTMENTS_TRACKING_LABEL,
} from "./trackers";
import {
  APPOINTMENT_STATUSES,
  availableActions,
  type AppointmentStatus,
} from "@/lib/appointments";

/*
 * Appointments panel (issue #124 panel 2) — HONEST PLACEHOLDER.
 *
 * The appointments backend does not exist: audited contracts/ (identity,
 * vehicles, jobs, dispatch, inspections, payments), every cmd/* service and
 * every migration chain — there is no appointments aggregate, endpoint or
 * table anywhere. Inventing one would require new APIs and migrations,
 * which this MVP forbids. The lifecycle the panel WILL drive is already
 * designed, pure and unit-tested (src/lib/appointments.ts) and previewed
 * here so the UX contract is reviewable; the panel ships as a pending-
 * capability card, the established Command MVP pattern (issue #123's
 * incidents panel).
 */
export function AppointmentsPanel() {
  return (
    <PanelSection
      id="appointments"
      title="Appointments calendar"
      description="Create, confirm and complete appointments tied to customer service requests — the day/week view for front desk and service managers."
    >
      <PendingCapabilityState
        capability="the appointments API"
        trackingIssue={APPOINTMENTS_TRACKING_ISSUE}
        trackingLabel={APPOINTMENTS_TRACKING_LABEL}
        detail="The backend has no appointments endpoints, storage or contracts yet (verified across contracts/ and every cmd/* service during the issue #124 API gap check). This console does not invent APIs, so the calendar ships disabled rather than showing fake appointments."
      />
      <div className="mt-4 rounded-md border border-slate-200 p-3">
        <h3 className="text-xs font-semibold uppercase tracking-wide text-slate-600">
          The lifecycle this panel will drive (designed + unit-tested, not yet wired)
        </h3>
        <ul className="mt-2 grid gap-2 sm:grid-cols-2">
          {APPOINTMENT_STATUSES.map((status) => (
            <li
              key={status}
              className="rounded-md border border-slate-200 bg-slate-50 p-3"
              data-testid={`appointment-state-${status}`}
            >
              <p className="text-sm font-semibold capitalize text-slate-800">{status}</p>
              <p className="mt-1 text-xs text-slate-600">
                {actionsLabel(status)}
              </p>
            </li>
          ))}
        </ul>
        <p className="mt-3 text-xs leading-relaxed text-slate-500">
          requested → confirm → confirmed → complete → completed; cancel is available until a
          terminal state. Every move will be enforced server-side when the API lands (append-only
          transition discipline, mirroring the jobs state machine) — the console renders
          affordances only and never grants permission.
        </p>
      </div>
    </PanelSection>
  );
}

function actionsLabel(status: AppointmentStatus): string {
  const actions = availableActions(status);
  if (actions.length === 0) {
    return "Terminal — no further changes.";
  }
  return `Actions: ${actions.join(", ")}.`;
}
