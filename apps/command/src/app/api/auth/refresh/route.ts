import { NextResponse, type NextRequest } from "next/server";
import { commandConfig } from "@/lib/config";
import { withBasePath } from "@/lib/base-path";
import { upstreamFetch, UpstreamError } from "@/lib/api/upstream";
import { problemResponse } from "@/lib/api/problem";
import {
  ACCESS_COOKIE,
  REFRESH_COOKIE,
  accessCookieSpec,
  clearedCookieSpec,
  cookieSecure,
  refreshCookieSpec,
  verifyToken,
} from "@/lib/auth/session";
import { isStaff } from "@/lib/auth/staff";

/*
 * POST /api/auth/refresh — rotate the session server-side.
 *
 * The refresh token lives in an httpOnly cookie scoped to /api/auth, so
 * only this handler can present it (identity rotates on every use; reuse of
 * a rotated token revokes the session — the theft signal, ADR-0004). The
 * NEW access token is re-verified and the staff allowlist re-checked: a
 * role change is picked up at rotation time. Called by the console's
 * keep-alive loop before the 15-minute access TTL lapses.
 */

export const runtime = "nodejs";
export const dynamic = "force-dynamic";

interface AccessResponse {
  access_token: string;
  refresh_token: string;
  expires_at: string;
}

export async function POST(request: NextRequest) {
  const presented = request.cookies.get(REFRESH_COOKIE)?.value ?? "";
  if (presented === "") {
    return problemResponse(401, "no_session", "No Command session to refresh");
  }

  const config = commandConfig();
  let access: AccessResponse;
  try {
    access = await upstreamFetch<AccessResponse>(config.identityUrl, "/v1/auth/refresh", {
      method: "POST",
      body: { refresh_token: presented },
      timeoutMs: config.upstreamTimeoutMs,
    });
  } catch (error) {
    if (error instanceof UpstreamError && error.status === 401) {
      // Rotated/revoked/unknown refresh token: session gone. Fail closed.
      const secure = cookieSecure();
      const response = problemResponse(401, "session_expired", "Command session expired");
      response.cookies.set(clearedCookieSpec(ACCESS_COOKIE, "/", secure));
      response.cookies.set(clearedCookieSpec(REFRESH_COOKIE, withBasePath("/api/auth"), secure));
      return response;
    }
    if (error instanceof UpstreamError && error.status === 0) {
      return problemResponse(502, "identity_unreachable", "The identity service is unreachable");
    }
    if (error instanceof UpstreamError) {
      return problemResponse(error.status, error.code, error.title);
    }
    return problemResponse(502, "identity_unreachable", "The identity service is unreachable");
  }

  const verified = await verifyToken(access.access_token);
  const secure = cookieSecure();
  if (!verified.ok || !isStaff(verified.claims.roles)) {
    // The rotated session no longer verifies or no longer carries a staff
    // role (grant changed/revoked): no console session.
    const response = problemResponse(403, "not_staff", "This session is no longer authorized for Motivra Command");
    response.cookies.set(clearedCookieSpec(ACCESS_COOKIE, "/", secure));
    response.cookies.set(clearedCookieSpec(REFRESH_COOKIE, "/api/auth", secure));
    return response;
  }

  const maxAge = Math.max(60, verified.claims.expiresAt - Math.floor(Date.now() / 1000));
  const response = NextResponse.json({ ok: true });
  response.cookies.set(accessCookieSpec(access.access_token, maxAge, secure));
  response.cookies.set(refreshCookieSpec(access.refresh_token, secure));
  return response;
}
