-- Append-only service history. Corrections are new records; existing rows
-- are never updated or deleted (see the trigger below).
CREATE TABLE vehicle_service_history (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    vehicle_id    uuid REFERENCES vehicles(id) ON DELETE CASCADE,
    event_type    text NOT NULL CHECK (event_type IN ('service','repair','inspection','mileage','incident','modification','note')),
    occurred_at   timestamptz NOT NULL,
    odometer_km   bigint NULL,
    summary       text NOT NULL,
    evidence_ref  text NOT NULL DEFAULT '',
    recorded_by   uuid NULL,
    created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX vehicle_service_history_vehicle_occurred_idx
    ON vehicle_service_history (vehicle_id, occurred_at DESC);

CREATE FUNCTION prevent_vehicle_service_history_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'vehicle_service_history is append-only';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER vehicle_service_history_append_only
    BEFORE UPDATE OR DELETE ON vehicle_service_history
    FOR EACH ROW EXECUTE FUNCTION prevent_vehicle_service_history_mutation();
