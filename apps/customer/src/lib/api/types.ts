/*
 * API types — hand-derived from the OpenAPI contracts.
 *
 * Sources of truth (do not invent fields):
 *   - contracts/identity/openapi.yaml (Motivra Identity API 1.0.0)
 *   - contracts/vehicles/openapi.yaml (Motivra Vehicle Identity API 1.0.0)
 *
 * When a contract changes, update these types in the same PR.
 */

/** contracts/identity/openapi.yaml — Role enum. */
export type Role =
  | "CUSTOMER"
  | "TECHNICIAN"
  | "DISPATCHER"
  | "GARAGE"
  | "FLEET_ADMIN"
  | "SUPPORT"
  | "FINANCE"
  | "ADMIN"
  | "SUPER_ADMIN";

/** contracts/identity/openapi.yaml — UserStatus enum. */
export type UserStatus = "active" | "suspended" | "deleted";

/** contracts/identity/openapi.yaml — LoginRequest (required: email, password). */
export interface LoginRequest {
  email: string;
  password: string;
  device_name?: string;
}

/** contracts/identity/openapi.yaml — RegisterRequest (required: email, password, full_name). */
export interface RegisterRequest {
  email: string;
  phone?: string;
  password: string;
  full_name: string;
  device_name?: string;
}

/** contracts/identity/openapi.yaml — RefreshRequest. */
export interface RefreshRequest {
  refresh_token: string;
}

/** contracts/identity/openapi.yaml — LogoutRequest. */
export interface LogoutRequest {
  refresh_token: string;
}

/** contracts/identity/openapi.yaml — AccessResponse. */
export interface AccessResponse {
  /** HS256 JWT (15 minutes) carrying sub, tenant_id, role, roles, sid, iss, aud, iat, exp. */
  access_token: string;
  /** Opaque 256-bit base64url token (30 days), rotated on every refresh. */
  refresh_token: string;
  /** Absolute expiry of the access token (date-time). */
  expires_at: string;
}

/** contracts/identity/openapi.yaml — User. */
export interface User {
  id: string;
  email: string;
  /** Empty when absent. */
  phone: string;
  full_name: string;
  status: UserStatus;
  roles: Role[];
  created_at: string;
}

/** contracts/identity/openapi.yaml — UserList. */
export interface UserList {
  users: User[];
  limit: number;
  offset: number;
}

/** contracts/vehicles/openapi.yaml — HistoryEvent.event_type enum. */
export type HistoryEventType =
  | "service"
  | "repair"
  | "inspection"
  | "mileage"
  | "incident"
  | "modification"
  | "note";

/** contracts/vehicles/openapi.yaml — Vehicle. VIN is the normalized 17-char ISO 3779 form. */
export interface Vehicle {
  id: string;
  /** Present when the vehicle belongs to an organization (fleet/dealer). */
  tenant_id?: string | null;
  owner_user_id: string;
  vin: string;
  make: string;
  model: string;
  year_of_manufacture: number;
  plate?: string;
  color?: string;
  mileage_latest_km: number;
  created_at: string;
  updated_at: string;
}

/** contracts/vehicles/openapi.yaml — CreateVehicleRequest (VIN is normalized server-side). */
export interface CreateVehicleRequest {
  vin: string;
  make: string;
  model: string;
  year_of_manufacture: number;
  plate?: string;
  color?: string;
}

/** contracts/vehicles/openapi.yaml — HistoryEvent (append-only; never mutated or deleted). */
export interface HistoryEvent {
  id: string;
  vehicle_id: string;
  event_type: HistoryEventType;
  summary: string;
  /** Reference to stored evidence (photos, OBD codes, measurements), if any. */
  evidence_ref?: string;
  occurred_at: string;
  odometer_km?: number | null;
  recorded_by?: string | null;
  created_at: string;
}

/** contracts/vehicles/openapi.yaml — Passport (read-only customer-facing summary). */
export interface Passport {
  /** Vehicle identifier. */
  id: string;
  tenant_id?: string | null;
  owner_user_id: string;
  vin: string;
  make: string;
  model: string;
  year_of_manufacture: number;
  plate?: string;
  color?: string;
  mileage_latest_km: number;
  /** Total number of append-only history events for the vehicle. */
  service_event_count: number;
  /** occurred_at of the most recent history event; null when none exist. */
  last_service_at?: string | null;
  created_at: string;
}

/** contracts/vehicles/openapi.yaml — MileageRequest. Odometer can never decrease. */
export interface MileageRequest {
  odometer_km: number;
}

/**
 * RFC 7807-style problem+json error body — shared shape across the identity
 * (contracts/identity/openapi.yaml — `Problem`) and vehicles
 * (contracts/vehicles/openapi.yaml — `Error`) contracts.
 */
export interface Problem {
  /** Stable machine code, e.g. validation_failed, unauthorized, conflict. */
  code: string;
  title: string;
  detail?: string;
  /** Field-level issues for validation failures. */
  errors?: Array<{ field: string; issue: string }>;
}
