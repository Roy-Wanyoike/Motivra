import { apiFetch } from "./client";
import type {
  CreateVehicleRequest,
  HistoryEvent,
  ListVehiclesParams,
  MileageRequest,
  Passport,
  Vehicle,
  VehicleList,
} from "./types";

/*
 * Vehicle identity client — endpoints verbatim from
 * contracts/vehicles/openapi.yaml (1.0.0). All endpoints require a Bearer
 * access token issued by the identity service.
 */

/** Endpoint map (documentation + UI copy); paths mirror the contract. */
export const VEHICLE_ENDPOINTS = {
  list: "GET /v1/vehicles",
  register: "POST /v1/vehicles",
  get: "GET /v1/vehicles/{vehicleID}",
  passport: "GET /v1/vehicles/{vehicleID}/passport",
  history: "GET /v1/vehicles/{vehicleID}/history",
  mileage: "POST /v1/vehicles/{vehicleID}/mileage",
} as const;

function vehiclePath(vehicleId: string, suffix = ""): string {
  return `/v1/vehicles/${encodeURIComponent(vehicleId)}${suffix}`;
}

/**
 * GET /v1/vehicles — one keyset page of the caller's vehicles, newest first.
 *
 * The listing scope is derived from the JWT claims server-side; pass the
 * previous page's `next_cursor` back as `cursor` to walk forward. The token
 * is optional here only so the UI can issue the request before the identity
 * wiring lands — an unauthenticated call surfaces the API's 401 in the
 * caller's error state, never a fake success.
 */
export function listVehicles(
  params: ListVehiclesParams = {},
  authToken?: string,
  signal?: AbortSignal,
): Promise<VehicleList> {
  const search = new URLSearchParams();
  if (params.limit !== undefined) {
    search.set("limit", String(params.limit));
  }
  if (params.cursor !== undefined) {
    search.set("cursor", params.cursor);
  }
  const query = search.size > 0 ? `?${search.toString()}` : "";
  return apiFetch<VehicleList>(`/v1/vehicles${query}`, { authToken, signal });
}

/** POST /v1/vehicles — register a vehicle (VIN normalized + ISO 3779 verified server-side). */
export function registerVehicle(
  request: CreateVehicleRequest,
  authToken: string,
  signal?: AbortSignal,
): Promise<Vehicle> {
  return apiFetch<Vehicle>("/v1/vehicles", {
    method: "POST",
    body: request,
    authToken,
    signal,
  });
}

/** GET /v1/vehicles/{vehicleID} — fetch one vehicle. */
export function getVehicle(
  vehicleId: string,
  authToken: string,
  signal?: AbortSignal,
): Promise<Vehicle> {
  return apiFetch<Vehicle>(vehiclePath(vehicleId), { authToken, signal });
}

/** GET /v1/vehicles/{vehicleID}/passport — read the read-only Vehicle Passport. */
export function getVehiclePassport(
  vehicleId: string,
  authToken: string,
  signal?: AbortSignal,
): Promise<Passport> {
  return apiFetch<Passport>(vehiclePath(vehicleId, "/passport"), {
    authToken,
    signal,
  });
}

/** GET /v1/vehicles/{vehicleID}/history — append-only history, newest first. */
export function listVehicleHistory(
  vehicleId: string,
  authToken: string,
  pagination: { limit?: number; offset?: number } = {},
  signal?: AbortSignal,
): Promise<HistoryEvent[]> {
  const params = new URLSearchParams();
  if (pagination.limit !== undefined) {
    params.set("limit", String(pagination.limit));
  }
  if (pagination.offset !== undefined) {
    params.set("offset", String(pagination.offset));
  }
  const query = params.size > 0 ? `?${params.toString()}` : "";
  return apiFetch<HistoryEvent[]>(vehiclePath(vehicleId, "/history") + query, {
    authToken,
    signal,
  });
}

/** POST /v1/vehicles/{vehicleID}/mileage — record an odometer reading (204; odometer never decreases). */
export function recordVehicleMileage(
  vehicleId: string,
  request: MileageRequest,
  authToken: string,
  signal?: AbortSignal,
): Promise<void> {
  return apiFetch<void>(vehiclePath(vehicleId, "/mileage"), {
    method: "POST",
    body: request,
    authToken,
    signal,
  });
}
