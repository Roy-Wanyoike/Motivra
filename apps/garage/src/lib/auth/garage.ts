/*
 * Garage identity — the multi-tenant isolation core of this console
 * (issue #124's headline security requirement).
 *
 * THE GARAGE ID IS NEVER CLIENT INPUT. The garage a staff member operates
 * is exclusively the `tenant_id` claim of their VERIFIED access token
 * (identity stamps tenant_id from the user's tenant-scoped role grants —
 * see backend/identity/tokens.go claimContext). No query parameter, request
 * body, header or cookie value can ever influence which garage's data is
 * requested: the server-side upstream calls carry only the session token,
 * and the backend derives the data boundary from the same claims
 * (backend/jobs/scope.go ReadScope, backend/payments/scope.go
 * ScopeFromClaims). Defense in depth: the console sends no garage
 * identifier at all, and the services would ignore it if it did.
 *
 * A verified GARAGE-role token WITHOUT a tenant_id claim cannot be bound
 * to any garage. Fail closed: the session is admitted (the person is
 * garage staff) but every data panel shows the "no garage bound" state —
 * never another garage's data, never a prompt to type one in.
 */

import type { AccessClaims } from "./claims";

export interface GarageIdentity {
  /**
   * The garage's tenant identifier — the verified token's tenant_id claim.
   * Empty string when the token carries no tenant binding (fail-closed
   * state; no panel may fetch data for this session).
   */
  tenantId: string;
  /** Convenience: tenantId !== "". */
  bound: boolean;
  /** Masked display form for the header (screen-share hygiene). */
  display: "bound" | "unbound";
}

export function garageIdentityFromClaims(claims: AccessClaims): GarageIdentity {
  const tenantId = typeof claims.tenantId === "string" ? claims.tenantId.trim() : "";
  return {
    tenantId,
    bound: tenantId !== "",
    display: tenantId !== "" ? "bound" : "unbound",
  };
}
