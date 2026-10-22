/*
 * Appointment lifecycle — the pure UI state machine for the Garage
 * appointments panel (issue #124).
 *
 * HONEST STATE: the appointments BACKEND does not exist yet (no endpoints,
 * no migrations anywhere in contracts/ or cmd/* — verified during the #124
 * API gap check; tracked in the backend child issue referenced by
 * APPOINTMENTS_TRACKING_ISSUE). This module is the console-side lifecycle
 * the panel will drive the moment that API ships: create (requested) →
 * confirm → complete, with cancellation from every non-terminal state.
 * It is pure, deterministic and unit-tested (tests/appointment-lifecycle.test.ts)
 * so the UX contract is reviewable NOW; the panel itself ships as an honest
 * "backend capability pending" card wired to nothing.
 *
 * Transition rules mirror the repo's job-state discipline (backend/jobs/
 * statemachine.go): forward-only, terminal states immutable, every move
 * will eventually be server-enforced — this client machine is for UI
 * affordances only and never grants permission by itself.
 */

export const APPOINTMENT_STATUSES = [
  "requested",
  "confirmed",
  "completed",
  "cancelled",
] as const;

export type AppointmentStatus = (typeof APPOINTMENT_STATUSES)[number];

export const APPOINTMENT_ACTIONS = [
  "confirm",
  "complete",
  "cancel",
] as const;

export type AppointmentAction = (typeof APPOINTMENT_ACTIONS)[number];

/** The legal transitions of the appointment lifecycle. */
const TRANSITIONS: Record<AppointmentStatus, Partial<Record<AppointmentAction, AppointmentStatus>>> = {
  requested: { confirm: "confirmed", cancel: "cancelled" },
  confirmed: { complete: "completed", cancel: "cancelled" },
  completed: {},
  cancelled: {},
};

/** True when the move is legal (false for unknown states/actions — fail closed). */
export function canTransition(
  from: AppointmentStatus,
  action: AppointmentAction,
): boolean {
  const target = TRANSITIONS[from]?.[action];
  return target !== undefined;
}

/** Apply the action; unknown/illegal moves return the state unchanged. */
export function transition(
  from: AppointmentStatus,
  action: AppointmentAction,
): AppointmentStatus {
  return TRANSITIONS[from]?.[action] ?? from;
}

/** The actions the UI offers for a given status (empty for terminal states). */
export function availableActions(status: AppointmentStatus): AppointmentAction[] {
  return APPOINTMENT_ACTIONS.filter((action) => canTransition(status, action));
}

/** True when the appointment can no longer change (completed/cancelled). */
export function isTerminal(status: AppointmentStatus): boolean {
  return availableActions(status).length === 0;
}
