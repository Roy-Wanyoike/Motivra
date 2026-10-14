-- Append-only session transition audit: every status move of an inspection
-- session is recorded here in the same transaction as the move. Rows are
-- never updated or deleted (tamper-evident history, same discipline as
-- job_transitions).
CREATE TABLE inspection_session_transitions (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    session_id  uuid NOT NULL REFERENCES inspection_sessions(id) ON DELETE CASCADE,
    from_status text NOT NULL,
    to_status   text NOT NULL,
    actor_id    uuid NULL,
    reason      text NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX inspection_session_transitions_session_idx
    ON inspection_session_transitions (session_id, created_at);

CREATE FUNCTION prevent_inspection_session_transitions_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'inspection_session_transitions is append-only';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER inspection_session_transitions_append_only
    BEFORE UPDATE OR DELETE ON inspection_session_transitions
    FOR EACH ROW EXECUTE FUNCTION prevent_inspection_session_transitions_mutation();
