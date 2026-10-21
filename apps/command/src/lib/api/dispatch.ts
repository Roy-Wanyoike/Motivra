/*
 * Dispatch read model — shapes and paths verbatim from
 * contracts/dispatch/openapi.yaml (listDispatchFactors, GET /v1/dispatch/factors).
 */

import { upstreamFetch } from "./upstream";

export interface FactorInfo {
  name: "technical_fit" | "proximity" | "availability" | "equipment" | "parts" | "performance";
  description: string;
  default_weight: number;
}

export interface FactorCatalogue {
  factors: FactorInfo[];
  default_weights: Record<string, number>;
}

/**
 * GET /v1/dispatch/factors — the static, explainable scoring catalogue.
 * Rendered beside the job board so dispatchers see exactly how assignment
 * ranking works (ADR: dispatch decisions are explainable by design).
 */
export async function fetchDispatchFactors(
  baseUrl: string,
  authToken: string,
  timeoutMs = 4000,
): Promise<FactorCatalogue> {
  return upstreamFetch<FactorCatalogue>(baseUrl, "/v1/dispatch/factors", {
    authToken,
    timeoutMs,
  });
}
