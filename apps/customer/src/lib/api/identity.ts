import { apiFetch } from "./client";
import type {
  AccessResponse,
  LoginRequest,
  LogoutRequest,
  RefreshRequest,
  RegisterRequest,
  User,
} from "./types";

/*
 * Identity service client — endpoints verbatim from
 * contracts/identity/openapi.yaml (1.0.0).
 */

/** Endpoint map (documentation + UI copy); paths mirror the contract. */
export const IDENTITY_ENDPOINTS = {
  register: "POST /v1/auth/register",
  login: "POST /v1/auth/login",
  refresh: "POST /v1/auth/refresh",
  logout: "POST /v1/auth/logout",
  profile: "GET /v1/users/me",
} as const;

/** POST /v1/auth/login — authenticate with email and password. */
export function login(
  request: LoginRequest,
  signal?: AbortSignal,
): Promise<AccessResponse> {
  return apiFetch<AccessResponse>("/v1/auth/login", {
    method: "POST",
    body: request,
    signal,
  });
}

/** POST /v1/auth/register — create an account, receive the first token pair. */
export function register(
  request: RegisterRequest,
  signal?: AbortSignal,
): Promise<AccessResponse> {
  return apiFetch<AccessResponse>("/v1/auth/register", {
    method: "POST",
    body: request,
    signal,
  });
}

/** POST /v1/auth/refresh — rotate a refresh session (presented token is revoked). */
export function refresh(
  request: RefreshRequest,
  signal?: AbortSignal,
): Promise<AccessResponse> {
  return apiFetch<AccessResponse>("/v1/auth/refresh", {
    method: "POST",
    body: request,
    signal,
  });
}

/** POST /v1/auth/logout — revoke the session behind a refresh token (204). */
export function logout(
  request: LogoutRequest,
  signal?: AbortSignal,
): Promise<void> {
  return apiFetch<void>("/v1/auth/logout", {
    method: "POST",
    body: request,
    signal,
  });
}

/** GET /v1/users/me — the authenticated caller's profile. */
export function getProfile(authToken: string, signal?: AbortSignal): Promise<User> {
  return apiFetch<User>("/v1/users/me", { authToken, signal });
}
