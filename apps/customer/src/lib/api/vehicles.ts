import { apiFetch } from "./client";
import type {
  CreateVehicleRequest,
  HistoryEvent,
  MileageRequest,
  Passport,
  Vehicle,
} from "./types";

/*
 * Vehicle identity client — endpoints verbatim from
 * contracts/vehicles/openapi.yaml (1.0.0). All endpoints require a Bearer
 * access token issued by the identity service.
 */

/** Endpoint map (documentation + UI copy); paths mirror the contract. */
export const VEHICLE_ENDPOINTS = {
  register: "POST /v1/vehicles",
  get: "GET /v1/vehicles/{vehicleID}",
  passport: "GET /v1/vehicles/{vehicleID}/passport",
  history: "GET /v1/vehicles/{vehicleID}/history",
  mileage: "POST /v1/vehicles/{vehicleID}/mileage",
} as const;

function vehiclePath(vehicleId: string, suffix = ""): string {
  return `/v1/vehicles/${encodeURIComponent(vehicleId)}${suffix}`;
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
