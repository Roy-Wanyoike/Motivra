-- Jobs domain: job_transitions is documented append-only ("never updated
-- or deleted", 000004 header) — the tamper-evident job state history the
-- dispatch and dispute flows rely on. Enforce it at the database level
-- (integrity issue #83), mirroring vehicle_service_history.

CREATE FUNCTION prevent_job_transitions_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'job_transitions is append-only';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER job_transitions_append_only
    BEFORE UPDATE OR DELETE ON job_transitions
    FOR EACH ROW EXECUTE FUNCTION prevent_job_transitions_mutation();
