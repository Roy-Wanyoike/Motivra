/*
 * Motivra Command route proxy (Next.js 16 middleware — the `proxy.ts`
 * convention) — the deny-by-default gate for the staff-only console.
 *
 *   anonymous            → redirect to /login (no console content leaks)
 *   invalid/expired      → redirect to /login, session cookies cleared
 *   authenticated, non-staff → 403 (static document, no console content)
 *   authenticated staff  → pass (pages re-verify independently)
 *
 * Public surface: /login and /api/auth/* only (the auth handlers re-verify
 * everything they receive — being public paths does not make them trustful).
 *
 * Node.js runtime: the proxy convention always runs on Node.js (no
 * `runtime` export — Next 16 rejects route segment config in proxy.ts),
 * which the verifier needs for WebCrypto plus runtime env vars (COMMAND_*
 * are deployment values, not build-time constants).
 */

import { NextResponse, type NextRequest } from "next/server";
import { commandConfig } from "@/lib/config";
import { withBasePath } from "@/lib/base-path";
import { verifyAccessToken } from "@/lib/auth/claims";
import { cookieSecure, ACCESS_COOKIE, clearedCookieSpec, REFRESH_COOKIE } from "@/lib/auth/session";
import { decideRoute, forbiddenDocument, type SessionState } from "@/lib/auth/guard";
import { isStaff } from "@/lib/auth/staff";

export const config = {
  matcher: ["/((?!_next/static|_next/image|favicon.ico).*)"],
};

function sessionCookieState(request: NextRequest): { present: boolean; value: string } {
  const value = request.cookies.get(ACCESS_COOKIE)?.value ?? "";
  return { present: value !== "", value };
}

export default async function proxy(request: NextRequest) {
  const { pathname, search } = request.nextUrl;

  const { present, value } = sessionCookieState(request);
  let session: SessionState = { present, valid: false, staff: false };

  if (present) {
    const config = commandConfig();
    const result = await verifyAccessToken(value, {
      jwksUrl: config.jwksUrl,
      issuer: config.jwtIssuer,
      audience: config.jwtAudience,
      allowHS256: config.allowHS256,
      hs256Secret: config.hs256Secret,
    });
    session = {
      present: true,
      valid: result.ok,
      staff: result.ok && isStaff(result.claims.roles),
    };
  }

  const decision = decideRoute(pathname, search, session);

  if (decision.kind === "allow") {
    return NextResponse.next();
  }

  if (decision.kind === "forbidden") {
    return new NextResponse(forbiddenDocument(), {
      status: 403,
      headers: { "content-type": "text/html; charset=utf-8" },
    });
  }

  // redirect-login: strip any stale/invalid session cookies on the way out
  // so the next request skips verification entirely.
  const target = request.nextUrl.clone();
  target.pathname = decision.location.split("?")[0] || "/login";
  target.search = decision.location.includes("?")
    ? decision.location.slice(decision.location.indexOf("?"))
    : "";
  const response = NextResponse.redirect(target);
  if (present && !session.valid) {
    const secure = cookieSecure();
    response.cookies.set(clearedCookieSpec(ACCESS_COOKIE, "/", secure));
    response.cookies.set(clearedCookieSpec(REFRESH_COOKIE, withBasePath("/api/auth"), secure));
  }
  return response;
}
