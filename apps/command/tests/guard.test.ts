/*
 * The Command RBAC matrix (issue #123 required gate).
 *
 * Deny-by-default, proven case by case:
 *
 *   anonymous            → redirect to /login (next preserved, no data)
 *   present-but-invalid  → redirect to /login (session cookies cleared by the proxy)
 *   authenticated staff  → allowed on console routes
 *   authenticated, non-staff → 403 on EVERY console route
 *   public paths         → reachable by everyone (login + auth API)
 *
 * The staff allowlist is also pinned here: exactly DISPATCHER, ADMIN,
 * SUPER_ADMIN — the jobs service' operator read scope. Any other role
 * (CUSTOMER, TECHNICIAN, SUPPORT, …) is non-staff for Command.
 */

import { describe, expect, it } from "vitest";
import { decideRoute, forbiddenDocument, isPublicPath } from "@/lib/auth/guard";
import { isStaff, STAFF_ROLES } from "@/lib/auth/staff";

const CONSOLE_ROUTES = ["/", "/?status=ESCALATED", "/anything", "/jobs", "/settings"];

describe("isStaff — the staff allowlist", () => {
  it("admits exactly the operator roles, singly and combined", () => {
    for (const role of ["DISPATCHER", "ADMIN", "SUPER_ADMIN"]) {
      expect(isStaff([role]), role).toBe(true);
    }
    expect(isStaff(["CUSTOMER", "DISPATCHER"])).toBe(true);
    expect(isStaff(["SUPPORT", "TECHNICIAN", "SUPER_ADMIN"])).toBe(true);
  });

  it("denies every non-staff role", () => {
    for (const role of [
      "CUSTOMER",
      "TECHNICIAN",
      "SUPPORT",
      "FINANCE",
      "GARAGE",
      "FLEET_ADMIN",
      "dispatcher", // case-sensitive: claim values are upper-case enums
      "",
    ]) {
      expect(isStaff([role]), role).toBe(false);
    }
  });

  it("fails closed on empty, malformed or missing role lists", () => {
    expect(isStaff([])).toBe(false);
    expect(isStaff(undefined as unknown as string[])).toBe(false);
    expect(isStaff(null as unknown as string[])).toBe(false);
    expect(isStaff("DISPATCHER" as unknown as string[])).toBe(false);
    expect(isStaff([42] as unknown as string[])).toBe(false);
  });

  it("exposes exactly the three staff roles", () => {
    expect([...STAFF_ROLES].sort()).toEqual(["ADMIN", "DISPATCHER", "SUPER_ADMIN"]);
  });
});

describe("decideRoute — the RBAC decision matrix", () => {
  const anonymous = { present: false, valid: false, staff: false };
  const invalid = { present: true, valid: false, staff: false };
  const staff = { present: true, valid: true, staff: true };
  const nonStaff = { present: true, valid: true, staff: false };

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

  it("allows authenticated staff through console routes", () => {
    for (const route of CONSOLE_ROUTES) {
      const [pathname, search = ""] = route.split(/(?=\?)/);
      expect(decideRoute(pathname, search, staff), route).toEqual({ kind: "allow" });
    }
  });

  it("forbids authenticated NON-STAFF callers on every console route", () => {
    for (const route of CONSOLE_ROUTES) {
      const [pathname, search = ""] = route.split(/(?=\?)/);
      expect(decideRoute(pathname, search, nonStaff), route).toEqual({ kind: "forbidden" });
    }
  });

  it("keeps the login page and auth API public for every session state", () => {
    for (const session of [anonymous, invalid, nonStaff, staff]) {
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
    expect(doc).toContain("DISPATCHER");
    expect(doc).not.toContain("/api/");
    expect(doc).not.toContain("token");
  });
});
