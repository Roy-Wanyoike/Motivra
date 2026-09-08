-- Service requests: the customer-facing intake record. A request is either
-- still 'received' (nothing built from it yet), 'converted' (a job exists),
-- or 'cancelled'.
CREATE TABLE service_requests (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_id  uuid NOT NULL,
    vehicle_id   uuid NULL,
    description  text NOT NULL,
    location_lat double precision NULL,
    location_lng double precision NULL,
    address_text text NOT NULL DEFAULT '',
    status       text NOT NULL DEFAULT 'received' CHECK (status IN ('received', 'converted', 'cancelled')),
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);
