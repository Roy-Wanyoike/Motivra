/*
 * Route-access classification — the pure decision core of Command's
 * deny-by-default gate.
 *
 * The proxy (src/proxy.ts) and the per-page guard (src/lib/auth/page-guard.ts)
 * both reduce the live session to a SessionState and ask decideRoute() what
 * may happen. Keeping the decision pure makes the RBAC matrix directly
 * testable (tests/guard.test.ts): staff pass, non-staff get 403 on every
 * route, anonymous users are redirected to login with a `next` target.
 */

export type RouteKind = "page" | "public";

export interface SessionState {
  /** A session cookie was present. */
  present: boolean;
  /** The cookie held a token that verified (signature, expiry, iss/aud). */
  valid: boolean;
  /** The verified caller carries at least one staff role. */
  staff: boolean;
}

export type AccessDecision =
  | { kind: "allow" }
  | { kind: "redirect-login"; location: string }
  | { kind: "forbidden" };

/** Paths reachable without any session. */
export const PUBLIC_PATH_PREFIXES = ["/login", "/api/auth"] as const;

export function isPublicPath(pathname: string): boolean {
  return PUBLIC_PATH_PREFIXES.some(
    (prefix) => pathname === prefix || pathname.startsWith(`${prefix}/`),
  );
}

const STAFF_HOME = "/";

/**
 * Decide what may happen to a request for `pathname` (+ its query `search`)
 * given the session state.
 *
 *  - public paths (login, auth API) are always reachable;
 *  - anonymous or invalid sessions are redirected to /login (no data
 *    exposure — the redirect response carries no console content) with a
 *    `next` target preserving the full path + query;
 *  - authenticated non-staff callers get 403 on every console route;
 *  - authenticated staff callers pass (upstream RBAC still applies per
 *    panel — defense in depth, ADR-0004's two-layer rule).
 */
export function decideRoute(
  pathname: string,
  search: string,
  session: SessionState,
  options: { loginPath?: string } = {},
): AccessDecision {
  const loginPath = options.loginPath ?? "/login";
  if (isPublicPath(pathname)) {
    return { kind: "allow" };
  }
  if (!session.present || !session.valid) {
    const full = `${pathname}${search}`;
    return {
      kind: "redirect-login",
      location: `${loginPath}?next=${encodeURIComponent(full === "" ? STAFF_HOME : full)}`,
    };
  }
  if (!session.staff) {
    return { kind: "forbidden" };
  }
  return { kind: "allow" };
}

/** The static 403 document served to authenticated non-staff callers. */
export function forbiddenDocument(): string {
  return `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>403 — Not authorized · Motivra Command</title>
<style>
  :root { color-scheme: light; }
  body { margin: 0; min-height: 100vh; display: flex; align-items: center; justify-content: center;
         background: #f8fafc; color: #334155; font-family: ui-sans-serif, system-ui, sans-serif; }
  main { max-width: 30rem; margin: 1rem; padding: 2rem; background: #fff; border: 1px solid #e2e8f0;
         border-radius: 0.5rem; box-shadow: 0 1px 2px rgba(15, 23, 42, 0.06); }
  h1 { font-size: 1.25rem; margin: 0 0 0.25rem; color: #0f172a; }
  .kicker { font-size: 0.75rem; font-weight: 600; letter-spacing: 0.1em; text-transform: uppercase;
            color: #b45309; margin: 0 0 0.5rem; }
  p { line-height: 1.6; font-size: 0.875rem; margin: 0.5rem 0; }
  code { background: #f1f5f9; border-radius: 0.25rem; padding: 0.1rem 0.3rem; font-size: 0.8em; }
</style>
</head>
<body>
<main>
  <p class="kicker">Motivra Command</p>
  <h1>403 — Not authorized</h1>
  <p>Motivra Command is the staff-only internal operations console. Your
  account authenticated successfully but does not carry a Command staff role.</p>
  <p>Staff roles today: <code>DISPATCHER</code>, <code>ADMIN</code>,
  <code>SUPER_ADMIN</code>. If you believe this is an error, contact a
  Motivra administrator — role grants live in the identity service, and this
  console cannot widen them.</p>
</main>
</body>
</html>
`;
}
