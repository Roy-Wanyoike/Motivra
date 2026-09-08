-- Vehicle Passport (docs/GLOSSARY.md): customer-facing, read-only summary of
-- a vehicle's verified identity assembled from the registry and its history.
-- Never edited directly.
CREATE VIEW vehicle_passports AS
SELECT
    v.id                   AS vehicle_id,
    v.tenant_id,
    v.owner_user_id,
    v.vin_normalized,
    v.make,
    v.model,
    v.year_of_manufacture,
    v.plate,
    v.color,
    v.mileage_latest_km,
    (SELECT count(*)
       FROM vehicle_service_history h
      WHERE h.vehicle_id = v.id)          AS service_event_count,
    (SELECT max(h.occurred_at)
       FROM vehicle_service_history h
      WHERE h.vehicle_id = v.id)          AS last_service_at,
    v.created_at
FROM vehicles v;
