-- Job assignments: one row per dispatch attempt of a job to a technician.
-- A reassignment appends a new 'assigned' row; only the latest row per job
-- is operationally live.
CREATE TABLE job_assignments (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    job_id         uuid NOT NULL REFERENCES jobs(id),
    technician_id  uuid NOT NULL,
    assigned_by    uuid NULL,
    accepted_at    timestamptz NULL,
    status         text NOT NULL DEFAULT 'assigned' CHECK (status IN ('assigned', 'accepted', 'declined', 'reassigned')),
    created_at     timestamptz NOT NULL DEFAULT now()
);
