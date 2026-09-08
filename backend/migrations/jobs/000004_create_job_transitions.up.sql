-- Job transitions: append-only audit log of every status change. Never
-- updated or deleted; readers query by (job_id, created_at).
CREATE TABLE job_transitions (
    id          bigserial PRIMARY KEY,
    job_id      uuid NOT NULL REFERENCES jobs(id),
    from_status text NOT NULL,
    to_status   text NOT NULL,
    actor_id    uuid NULL,
    reason      text NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_job_transitions_job_id_created_at ON job_transitions (job_id, created_at);
