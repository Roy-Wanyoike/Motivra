/*
 * The Motivra Garage console role allowlist.
 *
 * Deny-by-default: a caller reaches any Garage route only when their
 * verified access token carries the GARAGE role (checked against the
 * `roles` claim — the caller's FULL role list, never the single
 * highest-priority `role` claim alone).
 *
 * The allowlist is deliberately EXACTLY the garage persona (issue #124):
 * the console is the partner garage's own workspace, and every panel is
 * scoped to data the token's tenant_id claim bounds. Operators have their
 * own console (apps/command). Widening this list to any operator role is
 * an identity RBAC-matrix decision first — the console adds a gate, never
 * widens one (ADR-0004). The payments service already recognizes GARAGE as
 * a money-mover principal (backend/payments/scope.go RoleGarage), and the
 * jobs domain's garage-scope extension is tracked in the backend backlog
 * (see apps/garage README "Honest status").
 */
export const GARAGE_ROLES: readonly string[] = ["GARAGE"];

/** Report whether the given role list grants Garage console access. */
export function isGarageStaff(roles: readonly string[]): boolean {
  if (!Array.isArray(roles)) {
    return false;
  }
  return roles.some((role) => GARAGE_ROLES.includes(role));
}
