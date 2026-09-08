-- Vehicle registry: canonical vehicle identity. One vehicle, one identity.
CREATE TABLE vehicles (
    id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id            uuid NULL,
    owner_user_id        uuid NOT NULL,
    vin_normalized       text NOT NULL UNIQUE,
    make                 text NOT NULL,
    model                text NOT NULL,
    year_of_manufacture  integer NOT NULL CHECK (year_of_manufacture BETWEEN 1950 AND 2100),
    plate                text NOT NULL DEFAULT '',
    color                text NOT NULL DEFAULT '',
    mileage_latest_km    bigint NOT NULL DEFAULT 0,
    created_at           timestamptz NOT NULL DEFAULT now(),
    updated_at           timestamptz NOT NULL DEFAULT now()
);
