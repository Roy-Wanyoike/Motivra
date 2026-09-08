-- Jobs: the operational unit produced from a service request. Status moves
-- only through the legal edges enforced by the jobs state machine; the
-- job_transitions table is the append-only audit of every move.
CREATE TABLE jobs (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    service_request_id  uuid NULL REFERENCES service_requests(id),
    customer_id         uuid NOT NULL,
    vehicle_id          uuid NULL,
    status              text NOT NULL DEFAULT 'CREATED' CHECK (status IN ('CREATED', 'TRIAGING', 'DISPATCHING', 'ASSIGNED', 'ACCEPTED', 'EN_ROUTE', 'ARRIVED', 'INSPECTION', 'DIAGNOSIS', 'ESTIMATE', 'AWAITING_APPROVAL', 'APPROVED', 'REPAIRING', 'VERIFICATION', 'COMPLETED', 'CANCELLED', 'FAILED', 'ESCALATED')),
    problem_summary     text NOT NULL,
    technician_id       uuid NULL,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now()
);
