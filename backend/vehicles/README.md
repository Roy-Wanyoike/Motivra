# Backend — Vehicle Identity

Vehicle registry, VIN resolution, the Vehicle Passport and the append-only service history. The vehicles domain engineer owns this zone; see docs/ARCHITECTURE.md §2 — do not edit outside your zone.

## Responsibilities

- **Vehicle Registry** — canonical identity system for vehicles (ISO 3779 VIN normalization + check-digit validation). One vehicle, one identity.
- **Vehicle History** — append-only record of service events. Corrections are new records; history is never silently mutated.
- **Vehicle Passport** — customer-facing summary of a vehicle's verified identity, assembled from registry + history. Never edited directly.
- **Mileage tracking** — odometer readings that can go up, never down.

## Layout

| File | Responsibility |
|---|---|
| `vin.go` | ISO 3779 VIN normalization (`NormalizeVIN`) and validation (`ValidateVIN`) with the standard transliteration/weight tables and position-9 check digit |
| `models.go` | `Vehicle`, `HistoryEvent`, `Passport` domain models |
| `store.go` | `Store` interface + `PostgresStore` (pgx/v5) implementation; `ErrVehicleNotFound` sentinel |
| `service.go` | `Service` orchestration: validation, event publishing, error mapping to `platform.ErrX` |
| `http.go` | chi HTTP handlers, `Routes(...)`, DTOs, owner-or-admin mutation gate (`canMutate`) |
| `../migrations/vehicles/embed.go` | `vehiclesmigrations.FS` — the domain's own SQL migration chain |

## Database

Migrations live in `backend/migrations/vehicles` and are applied at boot with
`platform.MigrateUp(ctx, pool, vehiclesmigrations.FS, "schema_migrations_vehicles")`:

1. `vehicles` — registry table; `vin_normalized` UNIQUE; year CHECK 1950–2100.
2. `vehicle_components` — named components, `ON DELETE CASCADE`.
3. `vehicle_service_history` — **append-only**. A plpgsql trigger (`BEFORE UPDATE OR DELETE`) raises
   `vehicle_service_history is append-only` for any mutation. Corrections are new rows — enforced by
   the database, not by application discipline. Note: because the FK cascades, the trigger also fires
   on cascaded deletes, so deleting a vehicle while history rows exist is blocked by design.
4. `vehicle_passports` — read-only VIEW: registry fields + `service_event_count` + `last_service_at`.

Rollback: each migration ships a `.down.sql` (view → trigger/function → tables).

## HTTP API (OpenAPI: `contracts/vehicles/openapi.yaml`)

| Method | Path | Notes |
|---|---|---|
| POST | `/v1/vehicles` | 201; VIN normalized server-side; duplicate VIN → 409; validation → 422 |
| GET | `/v1/vehicles/{vehicleID}` | 200 / 404 |
| GET | `/v1/vehicles/{vehicleID}/passport` | 200 / 404 |
| GET | `/v1/vehicles/{vehicleID}/history` | newest first; `?limit` (default 50, cap 200) `?offset` |
| POST | `/v1/vehicles/{vehicleID}/mileage` | 204; owner or ADMIN/SUPER_ADMIN only (403); decrease → 409 |

Every endpoint requires authentication (`platform.RequireAuthenticated`).
Tenant scoping: the Store interface has no tenant filter yet, so reads are
identity-scoped only. Tenant-scoped query variants land with the multi-tenancy
hardening issue (ADR-0004) — filtering results in handlers is not acceptable
and is deliberately not done.

## VIN validation

`NormalizeVIN` uppercases, strips spaces/hyphens and rejects anything that is
not exactly 17 characters of A-Z/0-9, plus the confusable letters I, O, Q.
`ValidateVIN` additionally verifies the ISO 3779 check digit at position 9
(transliteration + weight tables, remainder 10 → `X`). Raw user input is never
persisted; only the normalized form is stored in `vin_normalized`.

## Events (ADR-0002)

Published via `platform.Publisher` on subject `motivra.vehicles.<event_type>`
(the publisher may be nil — wiring lands with the notifications wave):

| Event | When | Payload highlights |
|---|---|---|
| `vehicle.created.v1` | registration | vehicle_id, vin, make, model, year_of_manufacture, owner_user_id, mileage_latest_km |
| `vehicle.history.updated.v1` | any history event appended | vehicle_id, history_event_id, event_type, summary, occurred_at, odometer_km? |
| `vehicle.mileage.recorded.v1` | odometer reading recorded | vehicle_id, old_km, new_km |

## Do NOT

- Do not update or delete `vehicle_service_history` rows — the database itself refuses; corrections are new events.
- Do not edit the Vehicle Passport directly — it is a view, never a write target.
- Do not store raw VINs — store only the normalized form.
- Do not let the odometer decrease — reject with a conflict, never rewrite history.
- Do not import other domain packages; consume their events, not their code.
- Do not swallow errors — map them to `platform.ErrX` and let the platform render problem+json.
