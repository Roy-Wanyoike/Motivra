import { NextResponse, type NextRequest } from "next/server";
import { commandConfig } from "@/lib/config";
import { upstreamFetch } from "@/lib/api/upstream";
import {
  ACCESS_COOKIE,
  REFRESH_COOKIE,
  clearedCookieSpec,
  cookieSecure,
} from "@/lib/auth/session";
import { audit } from "@/lib/audit/audit";

/*
 * POST /api/auth/logout — revoke the identity session (best effort) and
 * clear both session cookies. Always answers 204: logout must succeed from
 * the operator's point of view even if the identity service is down (the
 * console session dies with the cookies either way; the refresh token, if
 * still live, dies with its 30-day window or the next rotation).
 */

export const runtime = "nodejs";
export const dynamic = "force-dynamic";

export async function POST(request: NextRequest) {
  const presented = request.cookies.get(REFRESH_COOKIE)?.value ?? "";
  if (presented !== "") {
    const config = commandConfig();
    try {
      await upstreamFetch(config.identityUrl, "/v1/auth/logout", {
        method: "POST",
        body: { refresh_token: presented },
        timeoutMs: config.upstreamTimeoutMs,
      });
    } catch {
      // Best effort — cookies are cleared regardless.
    }
  }

  const secure = cookieSecure();
  const response = new NextResponse(null, { status: 204 });
  response.cookies.set(clearedCookieSpec(ACCESS_COOKIE, "/", secure));
  response.cookies.set(clearedCookieSpec(REFRESH_COOKIE, "/api/auth", secure));

  // Actor is the presented refresh cookie's owner only insofar as we know:
  // logout carries no access token claim, so the actor is recorded as the
  // session holder anonymously (never log token material).
  audit({ action: "auth.logout", actor: "session-holder", route: "/api/auth/logout" });
  return response;
}
