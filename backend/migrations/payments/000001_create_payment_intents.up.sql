-- Payment intents are the aggregate root of the payments domain: the
-- declared intent to collect an integer amount of KES minor units from a
-- payer, its lifecycle status and its provider linkage.
--
-- Money discipline (issue #62): amount_minor is a STRICTLY POSITIVE
-- bigint (cents); there are no zero, negative or fractional amounts and
-- the currency is pinned to KES. The amount is immutable after creation —
-- a different price is a new intent, never an edit.
CREATE TABLE payment_intents (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id      uuid NULL,
    customer_id    uuid NULL,
    created_by     uuid NOT NULL,
    amount_minor   bigint NOT NULL CHECK (amount_minor > 0),
    currency       text   NOT NULL CHECK (currency = 'KES'),
    status         text   NOT NULL CHECK (status IN
                       ('CREATED','PROCESSING','SUCCEEDED','FAILED','CANCELED')),
    provider       text   NOT NULL,
    provider_ref   text   NULL,
    job_id         uuid NULL,
    vehicle_id     uuid NULL,
    description    text   NOT NULL DEFAULT '',
    failure_reason text   NULL,
    succeeded_at   timestamptz NULL,
    failed_at      timestamptz NULL,
    canceled_at    timestamptz NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX payment_intents_tenant_created_idx ON payment_intents (tenant_id, created_at DESC);
CREATE INDEX payment_intents_customer_idx ON payment_intents (customer_id);
CREATE INDEX payment_intents_created_by_idx ON payment_intents (created_by);
CREATE INDEX payment_intents_status_idx ON payment_intents (status);

-- Terminal integrity: a SUCCEEDED/FAILED/CANCELED intent never changes
-- status again, and its amount is frozen at creation. Retries are new
-- transactions on the provider side / new intents on the domain side —
-- never a mutation of settled money.
CREATE FUNCTION prevent_payment_intent_terminal_mutation() RETURNS trigger AS $$
BEGIN
    IF OLD.status IN ('SUCCEEDED','FAILED','CANCELED') AND NEW.status <> OLD.status THEN
        RAISE EXCEPTION 'payment_intents: intent % is terminal (%); settled money is immutable', OLD.id, OLD.status;
    END IF;
    IF NEW.amount_minor <> OLD.amount_minor OR NEW.currency <> OLD.currency THEN
        RAISE EXCEPTION 'payment_intents: amount and currency are immutable (issue #62 integer ledger)';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER payment_intents_integrity
    BEFORE UPDATE ON payment_intents
    FOR EACH ROW EXECUTE FUNCTION prevent_payment_intent_terminal_mutation();
