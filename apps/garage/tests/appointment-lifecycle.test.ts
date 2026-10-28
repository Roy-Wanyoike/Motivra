/*
 * The appointment lifecycle UI state machine (issue #124 panel 2).
 *
 * The appointments BACKEND does not exist yet (child issue tracked in
 * src/components/panels/trackers.ts); this module is the console-side
 * lifecycle the panel will drive once it ships. Pinned here case by case:
 *
 *   requested → confirm → confirmed → complete → completed
 *   requested|confirmed → cancel → cancelled
 *   completed, cancelled — terminal, immutable
 *
 * Unknown states/actions fail closed (no move). The machine renders UI
 * affordances only — the future API enforces every move server-side.
 */

import { describe, expect, it } from "vitest";
import {
  APPOINTMENT_ACTIONS,
  APPOINTMENT_STATUSES,
  availableActions,
  canTransition,
  isTerminal,
  transition,
  type AppointmentAction,
  type AppointmentStatus,
} from "@/lib/appointments";

const ALL_STATES = APPOINTMENT_STATUSES as readonly AppointmentStatus[];
const ALL_ACTIONS = APPOINTMENT_ACTIONS as readonly AppointmentAction[];

describe("appointment lifecycle — legal paths", () => {
  it("walks the happy path requested → confirmed → completed", () => {
    expect(transition("requested", "confirm")).toBe("confirmed");
    expect(transition("confirmed", "complete")).toBe("completed");
  });

  it("cancels from every non-terminal state", () => {
    expect(transition("requested", "cancel")).toBe("cancelled");
    expect(transition("confirmed", "cancel")).toBe("cancelled");
  });

  it("offers exactly the right actions per state", () => {
    expect(availableActions("requested")).toEqual(["confirm", "cancel"]);
    expect(availableActions("confirmed")).toEqual(["complete", "cancel"]);
    expect(availableActions("completed")).toEqual([]);
    expect(availableActions("cancelled")).toEqual([]);
  });

  it("marks completed and cancelled as terminal", () => {
    expect(isTerminal("completed")).toBe(true);
    expect(isTerminal("cancelled")).toBe(true);
    expect(isTerminal("requested")).toBe(false);
    expect(isTerminal("confirmed")).toBe(false);
  });
});

describe("appointment lifecycle — illegal moves fail closed", () => {
  it("rejects skipping the lifecycle (no create-to-complete jump)", () => {
    expect(canTransition("requested", "complete")).toBe(false);
    expect(transition("requested", "complete")).toBe("requested");
  });

  it("rejects reopening terminal states (append-only discipline)", () => {
    expect(canTransition("completed", "confirm")).toBe(false);
    expect(canTransition("completed", "cancel")).toBe(false);
    expect(canTransition("cancelled", "confirm")).toBe(false);
    expect(canTransition("cancelled", "complete")).toBe(false);
    expect(transition("completed", "confirm")).toBe("completed");
    expect(transition("cancelled", "complete")).toBe("cancelled");
  });

  it("rejects backwards moves (confirm is one-way)", () => {
    expect(canTransition("confirmed", "confirm")).toBe(false);
    expect(transition("confirmed", "confirm")).toBe("confirmed");
  });

  it("fails closed on unknown states and actions (no crash, no move)", () => {
    for (const action of ALL_ACTIONS) {
      expect(transition("unknown" as AppointmentStatus, action)).toBe("unknown");
      expect(canTransition("unknown" as AppointmentStatus, action)).toBe(false);
    }
    for (const state of ALL_STATES) {
      expect(transition(state, "frobnicate" as AppointmentAction)).toBe(state);
      expect(canTransition(state, "frobnicate" as AppointmentAction)).toBe(false);
    }
  });
});

describe("appointment lifecycle — vocabulary discipline", () => {
  it("uses the lowercase four-state machine documented for the API child issue", () => {
    expect([...ALL_STATES]).toEqual(["requested", "confirmed", "completed", "cancelled"]);
    expect([...ALL_ACTIONS]).toEqual(["confirm", "complete", "cancel"]);
  });
});
