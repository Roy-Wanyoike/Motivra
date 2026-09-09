/*
 * Interim access-token source for runtime API calls.
 *
 * The identity wiring (login → session persistence) has not landed yet — the
 * login form still shows its explicit "API integration pending" state (see
 * the README honesty table). Until it does, runtime callers read the Bearer
 * access token from localStorage under one well-known key, so that when auth
 * lands the same token feeds every client function unchanged.
 *
 * Honesty rules (repo policy — no mocked production flows):
 *   - When the token is absent the request still goes out unauthenticated
 *     and the API's 401 surfaces in the caller's error state. No fake data,
 *     no fake success.
 *   - This module never hard-codes credentials or endpoints.
 */

/** Well-known localStorage key for the HS256 access token (interim). */
export const ACCESS_TOKEN_STORAGE_KEY = "motivra.access_token";

/**
 * Read the Bearer access token for runtime API calls, or undefined when the
 * app runs server-side, storage is unavailable, or no token has been stored.
 */
export function readAccessToken(): string | undefined {
  if (typeof window === "undefined") {
    return undefined;
  }
  try {
    const value = window.localStorage.getItem(ACCESS_TOKEN_STORAGE_KEY);
    return value === null || value === "" ? undefined : value;
  } catch {
    // Storage can throw (privacy settings, sandboxed iframes) — fail closed.
    return undefined;
  }
}
