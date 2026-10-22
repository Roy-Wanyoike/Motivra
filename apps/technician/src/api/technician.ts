/**
 * Typed client for contracts/technician/openapi.yaml (DRAFT 0.1.0).
 *
 * Spec-first (repo principle 1): this file is the single place the Tech app
 * knows the technician pipeline API shapes. Phase 1 has NO backend — every
 * method honestly reports `backend-pending` when no API base URL is
 * configured (EXPO_PUBLIC_MOTIVRA_API_URL unset) instead of simulating a
 * server (repo principle 10: no incomplete features, no fake APIs).
 *
 * When a base URL IS configured (Phase 2, issue #166 / Phase 4, issue #168)
 * the same methods perform real fetches against the contract paths and map
 * RFC 7807 problem+json errors into typed results.
 */

export const TRADES = [
  'general_repair',
  'engine',
  'transmission',
  'electrical',
  'brake_systems',
  'suspension_steering',
  'air_conditioning',
  'diagnostics',
  'tyres',
  'body_work',
] as const;

export type Trade = (typeof TRADES)[number];

export const TRADE_LABELS: Record<Trade, string> = {
  general_repair: 'General repair',
  engine: 'Engine',
  transmission: 'Transmission',
  electrical: 'Electrical',
  brake_systems: 'Brake systems',
  suspension_steering: 'Suspension & steering',
  air_conditioning: 'Air conditioning',
  diagnostics: 'Diagnostics',
  tyres: 'Tyres',
  body_work: 'Body work',
};

/** contracts/technician/openapi.yaml → ServiceArea (coarse by design, PII-HANDLING.md). */
export interface ServiceArea {
  area_name: string;
  county: string;
}

/** contracts/technician/openapi.yaml → ProfileInput (request body of POST /v1/technician/profile). */
export interface ProfileInput {
  trade: Trade;
  specializations: Trade[];
  tools: string[];
  service_area: ServiceArea;
}

/** contracts/technician/openapi.yaml → TechnicianProfile. */
export interface TechnicianProfile extends ProfileInput {
  technician_id: string;
  updated_at: string;
}

/** contracts/technician/openapi.yaml → VerificationState. */
export type VerificationState = 'unverified' | 'assessment_pending' | 'verified';

/** contracts/technician/openapi.yaml → VerificationRecord. */
export interface VerificationRecord {
  id: string;
  technician_id: string;
  verifier_id: string;
  assessment_template_id: string;
  assessment_session_id: string;
  outcome: 'pass' | 'fail';
  notes: string | null;
  granted_at: string;
  expires_at: string | null;
  superseded_by: string | null;
}

/** contracts/technician/openapi.yaml → VerificationStatus. */
export interface VerificationStatus {
  status: VerificationState;
  updated_at: string;
  verification: VerificationRecord | null;
}

/** contracts/technician/openapi.yaml → TechnicianMe. */
export interface TechnicianMe {
  id: string;
  full_name: string;
  roles: string[];
  profile: TechnicianProfile | null;
  verification: VerificationStatus;
  created_at: string;
}

/** contracts/technician/openapi.yaml → EarningsLine (integer minor units, repo principle 4). */
export interface EarningsLine {
  job_id: string;
  payment_intent_id: string;
  transaction_id: string | null;
  gross_minor: number;
  fee_minor: number;
  net_minor: number;
  currency: 'KES';
  status: 'ledgered' | 'payout_pending' | 'paid_out';
  earned_at: string;
}

/** contracts/technician/openapi.yaml → EarningsSummary. */
export interface EarningsSummary {
  technician_id: string;
  currency: 'KES';
  balance_minor: number;
  lines: EarningsLine[];
}

/** contracts/technician/openapi.yaml → Problem (RFC 7807, application/problem+json). */
export interface Problem {
  code: string;
  title: string;
  detail?: string;
  errors?: { field: string; issue: string }[];
}

/**
 * Every read/mutation result is explicitly typed — the UI can never confuse
 * "no data yet" (backend pending) with "the server said no" (problem) or
 * "the network failed" (unreachable).
 */
export type ApiResult<T> =
  /** 2xx — contract-shaped payload. */
  | { kind: 'ok'; data: T }
  /** 4xx/5xx with an RFC 7807 problem body. */
  | { kind: 'problem'; status: number; problem: Problem }
  /** Phase-1 degraded mode: no backend configured (EXPO_PUBLIC_MOTIVRA_API_URL unset). */
  | { kind: 'backend-pending' }
  /** Network failure / non-problem response. */
  | { kind: 'unreachable'; message: string };

export interface TechnicianApiConfig {
  /** Base URL of the API gateway; null = Phase-1 degraded (backend pending). */
  baseUrl: string | null;
  /** Bearer access token (identity service). Phase 1: manual-testing token only. */
  accessToken: string | null;
}

export class TechnicianApiClient {
  private readonly config: TechnicianApiConfig;

  constructor(config: TechnicianApiConfig) {
    this.config = config;
  }

  /** True when this build has no backend to talk to (honest degraded mode). */
  get degraded(): boolean {
    return this.config.baseUrl === null;
  }

  /** POST /v1/technician/profile (contract: draft, Phase 2 — issue #166). */
  async submitProfile(input: ProfileInput, idempotencyKey: string): Promise<ApiResult<TechnicianProfile>> {
    if (this.config.baseUrl === null) return { kind: 'backend-pending' };
    return this.request<TechnicianProfile>('/v1/technician/profile', {
      method: 'POST',
      headers: { 'Idempotency-Key': idempotencyKey },
      body: JSON.stringify(input),
    });
  }

  /** GET /v1/technician/me (contract: draft, Phase 2 — issue #166). */
  async getMe(): Promise<ApiResult<TechnicianMe>> {
    if (this.config.baseUrl === null) return { kind: 'backend-pending' };
    return this.request<TechnicianMe>('/v1/technician/me', { method: 'GET' });
  }

  /** GET /v1/technician/verification-status (contract: draft, Phase 2 — issue #166). */
  async getVerificationStatus(): Promise<ApiResult<VerificationStatus>> {
    if (this.config.baseUrl === null) return { kind: 'backend-pending' };
    return this.request<VerificationStatus>('/v1/technician/verification-status', { method: 'GET' });
  }

  /** GET /v1/technician/earnings (contract: draft, Phase 4 — issue #168). */
  async getEarnings(): Promise<ApiResult<EarningsSummary>> {
    if (this.config.baseUrl === null) return { kind: 'backend-pending' };
    return this.request<EarningsSummary>('/v1/technician/earnings', { method: 'GET' });
  }

  private async request<T>(path: string, init: { method: string; headers?: Record<string, string>; body?: string }): Promise<ApiResult<T>> {
    const baseUrl = this.config.baseUrl;
    if (baseUrl === null) return { kind: 'backend-pending' };
    const headers: Record<string, string> = {
      Accept: 'application/json',
      ...(init.body !== undefined ? { 'Content-Type': 'application/json' } : {}),
      ...(init.headers ?? {}),
    };
    if (this.config.accessToken !== null) headers.Authorization = `Bearer ${this.config.accessToken}`;

    let response: Response;
    try {
      response = await fetch(`${baseUrl}${path}`, { method: init.method, headers, body: init.body });
    } catch (err) {
      return { kind: 'unreachable', message: err instanceof Error ? err.message : String(err) };
    }

    if (response.status >= 200 && response.status < 300) {
      try {
        const data = (await response.json()) as T;
        return { kind: 'ok', data };
      } catch (err) {
        return { kind: 'unreachable', message: `malformed 2xx body: ${err instanceof Error ? err.message : String(err)}` };
      }
    }

    const contentType = response.headers.get('content-type') ?? '';
    if (contentType.includes('application/problem+json')) {
      try {
        const problem = (await response.json()) as Problem;
        return { kind: 'problem', status: response.status, problem };
      } catch {
        return { kind: 'unreachable', message: `unreadable problem+json body (${response.status})` };
      }
    }
    return { kind: 'unreachable', message: `unexpected response ${response.status} (${contentType || 'no content-type'})` };
  }
}

/** Phase-1 default config: degraded unless EXPO_PUBLIC_MOTIVRA_API_URL is set. */
export function technicianApiConfigFromEnv(): TechnicianApiConfig {
  const baseUrl = process.env.EXPO_PUBLIC_MOTIVRA_API_URL;
  const accessToken = process.env.EXPO_PUBLIC_MOTIVRA_ACCESS_TOKEN;
  return {
    baseUrl: baseUrl && baseUrl.length > 0 ? baseUrl : null,
    accessToken: accessToken && accessToken.length > 0 ? accessToken : null,
  };
}
