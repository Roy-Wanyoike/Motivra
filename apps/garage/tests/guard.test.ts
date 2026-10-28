/*
 * The Garage RBAC matrix (issue #124 required gate) + the garage-identity
 * fail-closed rule.
 *
 * Deny-by-default, proven case by case:
 *
 *   anonymous            → redirect to /login (next preserved, no data)
 *   present-but-invalid  → redirect to /login (session cookies cleared by the proxy)
 *   authenticated GARAGE → allowed on console routes
 *   authenticated, any other role → 403 on EVERY console route
 *   public paths         → reachable by everyone (login + auth API)
 *
 * Identity (multi-tenant isolation, the headline risk): the garage a
 * session operates comes ONLY from the verified token's tenant_id claim;
 * a GARAGE token without one is bound to nothing and every data panel
 * must fail closed.
 */

import { describe, expect, it } from "vitest";
import { decideRoute, forbiddenDocument, isPublicPath } from "@/lib/auth/guard";
import { isGarageStaff, GARAGE_ROLES } from "@/lib/auth/staff";
import { garageIdentityFromClaims } from "@/lib/auth/garage";
import type { AccessClaims } from "@/lib/auth/claims";

const CONSOLE_ROUTES = ["/", "/?status=COMPLETED", "/anything", "/appointments", "/settings"];

describe("isGarageStaff — the role allowlist", () => {
  it("admits the GARAGE role, singly or combined with others", () => {
    expect(isGarageStaff(["GARAGE"])).toBe(true);
    expect(isGarageStaff(["CUSTOMER", "GARAGE"])).toBe(true);
    expect(isGarageStaff(["GARAGE", "FLEET_ADMIN"])).toBe(true);
  });

  it("denies every non-garage role — including the operator roles", () => {
    for (const role of [
      "CUSTOMER",
      "TECHNICIAN",
      "DISPATCHER",
      "ADMIN",
      "SUPER_ADMIN",
      "SUPPORT",
      "FINANCE",
      "FLEET_ADMIN",
      "garage", // case-sensitive: claim values are upper-case enums
      "",
    ]) {
      expect(isGarageStaff([role]), role).toBe(false);
    }
  });

  it("fails closed on empty, malformed or missing role lists", () => {
    expect(isGarageStaff([])).toBe(false);
    expect(isGarageStaff(undefined as unknown as string[])).toBe(false);
    expect(isGarageStaff(null as unknown as string[])).toBe(false);
    expect(isGarageStaff("GARAGE" as unknown as string[])).toBe(false);
    expect(isGarageStaff([42] as unknown as string[])).toBe(false);
  });

  it("exposes exactly the garage role", () => {
    expect([...GARAGE_ROLES]).toEqual(["GARAGE"]);
  });
});

describe("decideRoute — the RBAC decision matrix", () => {
  const anonymous = { present: false, valid: false, garageStaff: false };
  const invalid = { present: true, valid: false, garageStaff: false };
  const garageStaff = { present: true, valid: true, garageStaff: true };
  const nonGarage = { present: true, valid: true, garageStaff: false };

  it("redirects anonymous callers to login on every console route, preserving the target", () => {
    for (const route of CONSOLE_ROUTES) {
      const [pathname, search = ""] = route.split(/(?=\?)/);
      const decision = decideRoute(pathname, search, anonymous);
      expect(decision, route).toEqual({
        kind: "redirect-login",
        location: `/login?next=${encodeURIComponent(route)}`,
      });
    }
  });

  it("redirects present-but-invalid sessions exactly like anonymous ones (no content leaks)", () => {
    expect(decideRoute("/", "", invalid)).toEqual({
      kind: "redirect-login",
      location: "/login?next=%2F",
    });
    expect(decideRoute("/", "", anonymous)).toEqual(decideRoute("/", "", invalid));
  });

  it("allows authenticated garage staff through console routes", () => {
    for (const route of CONSOLE_ROUTES) {
      const [pathname, search = ""] = route.split(/(?=\?)/);
      expect(decideRoute(pathname, search, garageStaff), route).toEqual({ kind: "allow" });
    }
  });

  it("forbids authenticated NON-GARAGE callers on every console route (403, never data)", () => {
    for (const route of CONSOLE_ROUTES) {
      const [pathname, search = ""] = route.split(/(?=\?)/);
      expect(decideRoute(pathname, search, nonGarage), route).toEqual({ kind: "forbidden" });
    }
  });

  it("keeps the login page and auth API public for every session state", () => {
    for (const session of [anonymous, invalid, nonGarage, garageStaff]) {
      expect(decideRoute("/login", "", session).kind).toBe("allow");
      expect(decideRoute("/login", "?next=%2F", session).kind).toBe("allow");
      expect(decideRoute("/api/auth/login", "", session).kind).toBe("allow");
      expect(decideRoute("/api/auth/refresh", "", session).kind).toBe("allow");
      expect(decideRoute("/api/auth/logout", "", session).kind).toBe("allow");
    }
  });

  it("isPublicPath covers exactly the public surface (and nothing beneath the console)", () => {
    expect(isPublicPath("/login")).toBe(true);
    expect(isPublicPath("/api/auth/login")).toBe(true);
    expect(isPublicPath("/api/auth/logout")).toBe(true);
    expect(isPublicPath("/")).toBe(false);
    expect(isPublicPath("/login-page")).toBe(false); // prefix must match a full segment
    expect(isPublicPath("/api/auth")).toBe(true);
  });

  it("the static 403 document never leaks console content or role-widening paths", () => {
    const doc = forbiddenDocument();
    expect(doc).toContain("403 — Not authorized");
    expect(doc).toContain("GARAGE");
    expect(doc).not.toContain("/api/");
    expect(doc).not.toContain("token");
  });
});

describe("garageIdentityFromClaims — multi-tenant isolation", () => {
  const claimsWith = (tenantId: string): AccessClaims => ({
    sub: "a0000000-0000-4000-8000-000000000006",
    role: "GARAGE",
    roles: ["GARAGE"],
    sid: "session",
    tenantId,
    issuedAt: 0,
    expiresAt: 9999999999,
  });

  it("binds the session to the garage from the verified token's tenant_id claim", () => {
    const identity = garageIdentityFromClaims(claimsWith("11111111-2222-4333-8444-555566667777"));
    expect(identity.bound).toBe(true);
    expect(identity.tenantId).toBe("11111111-2222-4333-8444-555566667777");
    expect(identity.display).toBe("bound");
  });

  it("fails closed when the token carries no tenant claim (no garage → no data)", () => {
    const identity = garageIdentityFromClaims(claimsWith(""));
    expect(identity.bound).toBe(false);
    expect(identity.tenantId).toBe("");
    expect(identity.display).toBe("unbound");
  });

  it("treats whitespace-only tenant claims as unbound (fail closed)", () => {
    const identity = garageIdentityFromClaims(claimsWith("   "));
    expect(identity.bound).toBe(false);
  });
});
