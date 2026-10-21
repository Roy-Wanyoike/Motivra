/*
 * Command runtime configuration — every value comes from the environment,
 * read per call so tests and deployments can vary it freely. No secrets in
 * NEXT_PUBLIC_*: the browser bundle never sees any of this (all upstream
 * access is server-side; see docs/RUNBOOK.md §2c).
 *
 * Defaults match the RUNBOOK host-run topology (§2: services on :8080 per
 * `go run`, but the local convention for overlapping listeners in the
 * compose file is 8081-8086 on the host). Per-service URLs override the
 * gateway base; the gateway base (COMMAND_API_BASE_URL) overrides the
 * localhost defaults.
 */

function env(name: string): string {
  const value = process.env[name];
  return typeof value === "string" ? value.trim() : "";
}

function envBool(name: string): boolean {
  const value = env(name).toLowerCase();
  return value === "1" || value === "true" || value === "yes" || value === "on";
}

function envInt(name: string, fallback: number, minimum: number): number {
  const raw = env(name);
  if (raw === "") {
    return fallback;
  }
  const parsed = Number.parseInt(raw, 10);
  if (!Number.isFinite(parsed) || parsed < minimum) {
    return fallback;
  }
  return parsed;
}

export interface CommandConfig {
  /** Identity service base URL (login/refresh/logout + default JWKS). */
  identityUrl: string;
  /** Jobs service base URL (board + escalations). */
  jobsUrl: string;
  /** Dispatch service base URL (scoring factor catalogue). */
  dispatchUrl: string;
  /** Vehicles service base URL (health only). */
  vehiclesUrl: string;
  /** Inspections service base URL (health only). */
  inspectionsUrl: string;
  /** Payments service base URL (health only). */
  paymentsUrl: string;
  /** Optional gateway base used for any service without a specific URL. */
  apiBaseUrl: string;
  /** JWKS endpoint; empty = `${identityUrl}/.well-known/jwks.json`. */
  jwksUrl: string;
  /** Expected `iss` claim (mirrors MOTIVRA_JWT_ISSUER). */
  jwtIssuer: string;
  /** Expected `aud` claim (mirrors MOTIVRA_JWT_AUDIENCE). */
  jwtAudience: string;
  /** Legacy HS256 acceptance — migration window only, mirrors
   * MOTIVRA_JWT_ALLOW_HS256. Never enabled by default (ADR-0007). */
  allowHS256: boolean;
  /** Shared secret for the legacy HS256 path. Never set in production. */
  hs256Secret: string;
  /** Panel auto-refresh interval (seconds). */
  autoRefreshSeconds: number;
  /** Upstream fetch timeout (milliseconds). */
  upstreamTimeoutMs: number;
}

function withFallback(specific: string, gateway: string, localDefault: string): string {
  if (specific !== "") {
    return specific.replace(/\/+$/, "");
  }
  if (gateway !== "") {
    return gateway.replace(/\/+$/, "");
  }
  return localDefault;
}

export function commandConfig(): CommandConfig {
  const apiBaseUrl = env("COMMAND_API_BASE_URL").replace(/\/+$/, "");
  const identityUrl = withFallback(env("COMMAND_IDENTITY_URL"), apiBaseUrl, "http://localhost:8081");
  return {
    identityUrl,
    jobsUrl: withFallback(env("COMMAND_JOBS_URL"), apiBaseUrl, "http://localhost:8083"),
    dispatchUrl: withFallback(env("COMMAND_DISPATCH_URL"), apiBaseUrl, "http://localhost:8084"),
    vehiclesUrl: withFallback(env("COMMAND_VEHICLES_URL"), apiBaseUrl, "http://localhost:8082"),
    inspectionsUrl: withFallback(env("COMMAND_INSPECTIONS_URL"), apiBaseUrl, "http://localhost:8086"),
    paymentsUrl: withFallback(env("COMMAND_PAYMENTS_URL"), apiBaseUrl, "http://localhost:8085"),
    apiBaseUrl,
    jwksUrl: withFallback(env("COMMAND_JWKS_URL"), "", `${identityUrl}/.well-known/jwks.json`),
    jwtIssuer: env("COMMAND_JWT_ISSUER") === "" ? "motivra-identity" : env("COMMAND_JWT_ISSUER"),
    jwtAudience: env("COMMAND_JWT_AUDIENCE") === "" ? "motivra" : env("COMMAND_JWT_AUDIENCE"),
    allowHS256: envBool("COMMAND_JWT_ALLOW_HS256"),
    hs256Secret: env("COMMAND_JWT_SECRET"),
    autoRefreshSeconds: envInt("COMMAND_AUTO_REFRESH_SECONDS", 20, 5),
    upstreamTimeoutMs: envInt("COMMAND_UPSTREAM_TIMEOUT_MS", 4000, 250),
  };
}
