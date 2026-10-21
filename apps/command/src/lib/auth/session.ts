/*
 * Server-side session helpers for Motivra Command.
 *
 * The browser never holds a usable API token in JS: the (short-lived,
 * asymmetric) access token lives in an httpOnly cookie that only server
 * components and route handlers read, and the refresh token lives in an
 * httpOnly cookie scoped to /api/auth so panel code cannot touch it.
 * Every cookie read verifies the token against the identity JWKS before
 * any trust is placed in it.
 */

import { cookies } from "next/headers";
import { commandConfig } from "@/lib/config";
import { verifyAccessToken, type AccessClaims } from "./claims";
import { isStaff } from "./staff";

export const ACCESS_COOKIE = "command_access";
export const REFRESH_COOKIE = "command_refresh";

/** Refresh-token cookie lifetime (identity's 30-day refresh window). */
export const REFRESH_COOKIE_MAX_AGE = 30 * 24 * 60 * 60;

export interface Session {
  claims: AccessClaims;
  /** The verified access token (Bearer for upstream calls). */
  accessToken: string;
}

export interface CookieSpec {
  name: string;
  value: string;
  httpOnly: boolean;
  secure: boolean;
  sameSite: "lax";
  path: string;
  maxAge: number;
}

function baseCookie(secure: boolean): Pick<CookieSpec, "httpOnly" | "secure" | "sameSite"> {
  return { httpOnly: true, secure, sameSite: "lax" };
}

/** Cookie carrying the access token for the whole console. */
export function accessCookieSpec(token: string, maxAgeSeconds: number, secure: boolean): CookieSpec {
  return {
    name: ACCESS_COOKIE,
    value: token,
    path: "/",
    maxAge: Math.max(1, Math.min(maxAgeSeconds, 24 * 60 * 60)),
    ...baseCookie(secure),
  };
}

/** Cookie carrying the opaque refresh token — readable only by /api/auth/*. */
export function refreshCookieSpec(token: string, secure: boolean): CookieSpec {
  return {
    name: REFRESH_COOKIE,
    value: token,
    path: "/api/auth",
    maxAge: REFRESH_COOKIE_MAX_AGE,
    ...baseCookie(secure),
  };
}

export function clearedCookieSpec(name: string, path: string, secure: boolean): CookieSpec {
  return {
    name,
    value: "",
    path,
    maxAge: 0,
    ...baseCookie(secure),
  };
}

export function cookieSecure(): boolean {
  return process.env.NODE_ENV === "production";
}

/**
 * Read and verify the current session from cookies, or null when absent or
 * unverifiable. Every step fails closed: no cookie, malformed token,
 * bad signature, expired, wrong issuer/audience, or no roles → null.
 */
export async function readSession(): Promise<Session | null> {
  const store = await cookies();
  const token = store.get(ACCESS_COOKIE)?.value;
  if (!token) {
    return null;
  }
  const config = commandConfig();
  const result = await verifyAccessToken(token, {
    jwksUrl: config.jwksUrl,
    issuer: config.jwtIssuer,
    audience: config.jwtAudience,
    allowHS256: config.allowHS256,
    hs256Secret: config.hs256Secret,
  });
  if (!result.ok) {
    return null;
  }
  return { claims: result.claims, accessToken: token };
}

/**
 * Verify a presented access token (used by the auth route handlers, which
 * receive the raw token from the identity service rather than a cookie).
 */
export async function verifyToken(token: string) {
  const config = commandConfig();
  return verifyAccessToken(token, {
    jwksUrl: config.jwksUrl,
    issuer: config.jwtIssuer,
    audience: config.jwtAudience,
    allowHS256: config.allowHS256,
    hs256Secret: config.hs256Secret,
  });
}

/** Convenience: is the current caller authenticated staff? */
export async function sessionIsStaff(): Promise<boolean> {
  const session = await readSession();
  return session !== null && isStaff(session.claims.roles);
}
