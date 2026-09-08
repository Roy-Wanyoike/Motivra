-- Named components attached to a vehicle (engine, battery, tyres, ...).
CREATE TABLE vehicle_components (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    vehicle_id  uuid REFERENCES vehicles(id) ON DELETE CASCADE,
    name        text NOT NULL,
    category    text NOT NULL DEFAULT 'general',
    notes       text NOT NULL DEFAULT '',
    created_at  timestamptz DEFAULT now()
);
