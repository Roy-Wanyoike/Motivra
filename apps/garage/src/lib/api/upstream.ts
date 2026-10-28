/*
 * Server-side upstream client for the Motivra Go services.
 *
 * Garage's browser code NEVER calls the backend directly: every upstream
 * request happens here, in server components and route handlers, carrying
 * the caller's own short-lived access token from the httpOnly cookie (no
 * long-lived or service-wide tokens exist in this app). Errors surface as
 * UpstreamError with the RFC 7807 problem fields so panels can degrade
 * individually (issue #124: a degraded backend must never crash the
 * console).
 */

export class UpstreamError extends Error {
  readonly status: number;
  readonly code: string;
  readonly title: string;

  constructor(status: number, code: string, title: string) {
    super(title);
    this.name = "UpstreamError";
    this.status = status;
    this.code = code;
    this.title = title;
  }
}

/** True when the failure means "the backend itself is unreachable/down". */
export function isUnreachable(error: unknown): boolean {
  return error instanceof UpstreamError && error.status === 0;
}

interface UpstreamOptions {
  method?: "GET" | "POST";
  /** Bearer access token forwarded from the verified session. */
  authToken?: string;
  body?: unknown;
  timeoutMs?: number;
}

function emptyProblem(status: number): { code: string; title: string } {
  if (status === 0) {
    return { code: "upstream_unreachable", title: "The service did not respond" };
  }
  return { code: `upstream_${status}`, title: `The service answered HTTP ${status}` };
}

function isProblem(value: unknown): value is { code?: unknown; title?: unknown } {
  return typeof value === "object" && value !== null;
}

/**
 * One typed upstream request. Returns the parsed JSON body (undefined for
 * empty bodies). Every non-2xx and every network failure becomes an
 * UpstreamError — callers branch on status/code, never on exceptions.
 */
export async function upstreamFetch<T>(
  baseUrl: string,
  path: string,
  options: UpstreamOptions = {},
): Promise<T> {
  const { method = "GET", authToken, body, timeoutMs = 4000 } = options;
  const base = baseUrl.replace(/\/+$/, "");
  const headers: Record<string, string> = { Accept: "application/json" };
  if (body !== undefined) {
    headers["Content-Type"] = "application/json";
  }
  if (authToken) {
    headers.Authorization = `Bearer ${authToken}`;
  }

  let response: Response;
  try {
    response = await fetch(`${base}${path}`, {
      method,
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
      cache: "no-store",
      signal: AbortSignal.timeout(timeoutMs),
    });
  } catch {
    throw new UpstreamError(0, "upstream_unreachable", "The service did not respond");
  }

  if (response.status === 204) {
    return undefined as T;
  }

  const contentType = response.headers.get("content-type") ?? "";
  const isJson = contentType.includes("json");
  const parsed: unknown = isJson ? await response.json().catch(() => null) : null;

  if (!response.ok) {
    const fallback = emptyProblem(response.status);
    const problem = isProblem(parsed) &&
      typeof (parsed as { code?: unknown }).code === "string" &&
      typeof (parsed as { title?: unknown }).title === "string"
      ? (parsed as { code: string; title: string })
      : fallback;
    throw new UpstreamError(response.status, problem.code, problem.title);
  }

  if (!isJson) {
    return undefined as T;
  }
  return parsed as T;
}
