-- Payment refunds refund a succeeded intent (full refund first, issue
-- #62). Rows are guarded-update like transactions: REQUESTED moves to
-- COMPLETED or FAILED exactly once and every ledger column is immutable.
-- A partial unique index refuses a second REQUESTED refund while one is
-- in flight; the service refuses duplicates in every state.
CREATE TABLE payment_refunds (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    intent_id      uuid NOT NULL REFERENCES payment_intents(id) ON DELETE CASCADE,
    amount_minor   bigint NOT NULL CHECK (amount_minor > 0),
    currency       text   NOT NULL CHECK (currency = 'KES'),
    status         text   NOT NULL CHECK (status IN ('REQUESTED','COMPLETED','FAILED')),
    provider       text   NOT NULL,
    provider_ref   text NULL,
    reason         text   NOT NULL DEFAULT '',
    failure_reason text NULL,
    requested_by   uuid NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX payment_refunds_one_in_flight_idx
    ON payment_refunds (intent_id)
    WHERE status = 'REQUESTED';

CREATE INDEX payment_refunds_intent_idx ON payment_refunds (intent_id, created_at);

CREATE FUNCTION guard_payment_refund_update() RETURNS trigger AS $$
BEGIN
    IF NEW.intent_id <> OLD.intent_id
        OR NEW.amount_minor <> OLD.amount_minor
        OR NEW.currency <> OLD.currency
        OR NEW.provider <> OLD.provider
        OR NEW.created_at <> OLD.created_at THEN
        RAISE EXCEPTION 'payment_refunds: ledger columns are immutable';
    END IF;
    IF OLD.status = 'REQUESTED' AND NEW.status IN ('COMPLETED','FAILED') THEN
        RETURN NEW;
    END IF;
    RAISE EXCEPTION 'payment_refunds: status % -> % is not a legal ledger move', OLD.status, NEW.status;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER payment_refunds_guarded_update
    BEFORE UPDATE ON payment_refunds
    FOR EACH ROW EXECUTE FUNCTION guard_payment_refund_update();

CREATE FUNCTION prevent_payment_refund_delete() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'payment_refunds is an append-only ledger: rows are never deleted';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER payment_refunds_no_delete
    BEFORE DELETE ON payment_refunds
    FOR EACH ROW EXECUTE FUNCTION prevent_payment_refund_delete();
