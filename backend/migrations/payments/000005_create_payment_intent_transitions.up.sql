-- Payment intent transitions are the append-only audit log of the intent
-- state machine (mirrors inspection_session_transitions): every status
-- move writes exactly one row in the same transaction, and the rows are
-- never updated or deleted.
CREATE TABLE payment_intent_transitions (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    intent_id   uuid NOT NULL REFERENCES payment_intents(id) ON DELETE CASCADE,
    from_status text NULL,
    to_status   text NOT NULL,
    actor_id    uuid NULL,
    reason      text NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX payment_intent_transitions_intent_idx
    ON payment_intent_transitions (intent_id, created_at);

CREATE FUNCTION prevent_payment_intent_transitions_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'payment_intent_transitions is append-only; the ledger never forgets a move';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER payment_intent_transitions_append_only
    BEFORE UPDATE OR DELETE ON payment_intent_transitions
    FOR EACH ROW EXECUTE FUNCTION prevent_payment_intent_transitions_mutation();
