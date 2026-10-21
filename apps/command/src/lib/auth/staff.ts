/*
 * The Motivra Command staff allowlist.
 *
 * Deny-by-default: a caller reaches any Command route only when their
 * verified access token carries at least one of these roles (the `roles`
 * claim, the caller's FULL role list — never the single highest-priority
 * `role` claim alone).
 *
 * The set mirrors the jobs service's operator read scope (backend/jobs/
 * scope.go OperatorRoles): DISPATCHER, ADMIN and SUPER_ADMIN are exactly
 * the roles the cmd/jobs and cmd/dispatch read APIs authorize for the
 * board/escalation data this console renders. Adding a role here without
 * the matching upstream RBAC grant would render broken panels (403 from
 * the API), so the console allowlist is deliberately derived from the
 * upstream matrix (ADR-0004: permissions are enumerated server-side; the
 * console adds a gate, never widens one).
 *
 * SUPPORT / FINANCE / FLEET_ADMIN / GARAGE / CUSTOMER / TECHNICIAN are
 * non-staff for Command today: they receive 403 on every route. Extending
 * console access to SUPPORT (the escalations persona) first requires the
 * identity RBAC matrix to grant SUPPORT a read scope on the jobs API —
 * tracked as a follow-up decision, not a console-side change.
 */
export const STAFF_ROLES: readonly string[] = [
  "DISPATCHER",
  "ADMIN",
  "SUPER_ADMIN",
];

/** Report whether the given role list grants Command staff access. */
export function isStaff(roles: readonly string[]): boolean {
  if (!Array.isArray(roles)) {
    return false;
  }
  return roles.some((role) => STAFF_ROLES.includes(role));
}
