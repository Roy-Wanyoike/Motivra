import type { Problem } from "./types";

/*
 * Minimal typed fetch client for the Motivra API.
 *
 * - Base URL comes from NEXT_PUBLIC_API_BASE_URL (empty string = same-origin,
 *   which matches the "same-origin deployment behind the platform gateway"
 *   declared in both contract files' servers[*].url).
 * - Errors are surfaced as ApiError carrying the RFC 7807 problem+json body.
 * - No global side effects; nothing runs at build time.
 */

export class ApiError extends Error {
  readonly status: number;
  readonly problem: Problem;

  constructor(status: number, problem: Problem) {
    super(problem.title || problem.code || `API request failed with status ${status}`);
    this.name = "ApiError";
    this.status = status;
    this.problem = problem;
  }
}

export function apiBaseUrl(): string {
  return process.env.NEXT_PUBLIC_API_BASE_URL ?? "";
}

export interface ApiRequestOptions {
  method?: "GET" | "POST" | "PUT" | "PATCH" | "DELETE";
  /** Bearer access token (HS256 JWT issued by the identity service). */
  authToken?: string;
  body?: unknown;
  signal?: AbortSignal;
}

function emptyProblem(status: number): Problem {
  return { code: "internal_error", title: `Unexpected response (HTTP ${status})` };
}

function isProblem(value: unknown): value is Problem {
  return (
    typeof value === "object" &&
    value !== null &&
    typeof (value as { code?: unknown }).code === "string" &&
    typeof (value as { title?: unknown }).title === "string"
  );
}

/**
 * Perform one typed API request. Returns the parsed JSON body, or undefined
 * for 204 No Content responses (e.g. POST /v1/auth/logout).
 */
export async function apiFetch<T>(
  path: string,
  options: ApiRequestOptions = {},
): Promise<T> {
  const { method = "GET", authToken, body, signal } = options;

  const headers: Record<string, string> = { Accept: "application/json" };
  if (body !== undefined) {
    headers["Content-Type"] = "application/json";
  }
  if (authToken) {
    headers.Authorization = `Bearer ${authToken}`;
  }

  const response = await fetch(`${apiBaseUrl()}${path}`, {
    method,
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
    signal,
    // The platform is the source of truth; never serve stale client caches.
    cache: "no-store",
  });

  if (response.status === 204) {
    return undefined as T;
  }

  const contentType = response.headers.get("content-type") ?? "";
  const isJson = contentType.includes("json");

  if (!response.ok) {
    const parsed: unknown = isJson ? await response.json().catch(() => null) : null;
    const problem = isProblem(parsed) ? parsed : emptyProblem(response.status);
    throw new ApiError(response.status, problem);
  }

  if (!isJson) {
    return undefined as T;
  }
  return (await response.json()) as T;
}
