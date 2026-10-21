import { NextResponse, type NextRequest } from "next/server";
import { commandConfig } from "@/lib/config";
import { upstreamFetch, UpstreamError } from "@/lib/api/upstream";
import { problemResponse } from "@/lib/api/problem";
import { verifyToken } from "@/lib/auth/session";
import { accessCookieSpec, cookieSecure, refreshCookieSpec } from "@/lib/auth/session";
import { isStaff } from "@/lib/auth/staff";
import { audit } from "@/lib/audit/audit";

/*
 * POST /api/auth/login — staff sign-in for Motivra Command.
 *
 * Flow: credentials → identity service POST /v1/auth/login → the issued
 * access token is VERIFIED against the identity JWKS (signature, expiry,
 * iss/aud, claim shape) → the staff allowlist decides → cookies set or 403.
 * The browser never sees a token; cookies are httpOnly. Non-staff callers
 * get a problem+json 403 and no session. Every outcome is audit-logged.
 */

export const runtime = "nodejs";
export const dynamic = "force-dynamic";

interface AccessResponse {
  access_token: string;
  refresh_token: string;
  expires_at: string;
}

interface LoginBody {
  email?: unknown;
  password?: unknown;
}

export async function POST(request: NextRequest) {
  let body: LoginBody;
  try {
    body = (await request.json()) as LoginBody;
  } catch {
    return problemResponse(400, "invalid_body", "Request body must be JSON");
  }
  const email = typeof body.email === "string" ? body.email.trim().toLowerCase() : "";
  const password = typeof body.password === "string" ? body.password : "";
  if (email === "" || password === "" || email.length > 254 || password.length > 128) {
    return problemResponse(422, "validation_failed", "Email and password are required");
  }

  const config = commandConfig();

  let access: AccessResponse;
  try {
    access = await upstreamFetch<AccessResponse>(config.identityUrl, "/v1/auth/login", {
      method: "POST",
      body: { email, password },
      timeoutMs: config.upstreamTimeoutMs,
    });
  } catch (error) {
    if (error instanceof UpstreamError) {
      if (error.status === 0) {
        return problemResponse(502, "identity_unreachable", "The identity service is unreachable");
      }
      // Pass the identity service's own verdict through (401 invalid
      // credentials, 422 validation, 429 throttle…).
      return problemResponse(error.status, error.code, error.title);
    }
    return problemResponse(502, "identity_unreachable", "The identity service is unreachable");
  }

  const verified = await verifyToken(access.access_token);
  if (!verified.ok) {
    // The identity service issued a token Command cannot verify — a
    // deployment/keys drift, never a user error. Fail closed.
    console.error(JSON.stringify({ service: "command", kind: "login_verify_failed", reason: verified.reason }));
    return problemResponse(
      502,
      "token_unverifiable",
      "The identity service issued a token this console cannot verify",
    );
  }

  if (!isStaff(verified.claims.roles)) {
    audit({
      action: "auth.login_denied",
      actor: verified.claims.sub,
      route: "/api/auth/login",
      detail: { roles: verified.claims.roles.length },
    });
    return problemResponse(
      403,
      "not_staff",
      "This account does not carry a Motivra Command staff role",
    );
  }

  const secure = cookieSecure();
  const maxAge = Math.max(60, verified.claims.expiresAt - Math.floor(Date.now() / 1000));
  const response = NextResponse.json({
    sub: verified.claims.sub,
    role: verified.claims.role,
    expires_at: access.expires_at,
  });
  response.cookies.set(accessCookieSpec(access.access_token, maxAge, secure));
  response.cookies.set(refreshCookieSpec(access.refresh_token, secure));
  audit({
    action: "auth.login",
    actor: verified.claims.sub,
    route: "/api/auth/login",
    detail: { role: verified.claims.role },
  });
  return response;
}
