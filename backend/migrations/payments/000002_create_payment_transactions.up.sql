-- Payment transactions are the per-provider settlement attempts against
-- one intent. Rows are GUARDED-UPDATE, not freely mutable (issue #62
-- ledger integrity):
--   * intent_id, amount_minor, currency and provider are immutable;
--   * status may move exactly PENDING -> SUCCEEDED | FAILED (the state
--     machine in the code and this trigger agree);
--   * a terminal row can never change again and can never be deleted.
CREATE TABLE payment_transactions (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    intent_id      uuid NOT NULL REFERENCES payment_intents(id) ON DELETE CASCADE,
    provider       text NOT NULL,
    provider_ref   text NULL,
    provider_receipt text NULL,
    status         text NOT NULL CHECK (status IN ('PENDING','SUCCEEDED','FAILED')),
    amount_minor   bigint NOT NULL CHECK (amount_minor > 0),
    currency       text   NOT NULL CHECK (currency = 'KES'),
    failure_reason text NULL,
    occurred_at    timestamptz NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);

-- The provider reference is the webhook/query correlation handle: at most
-- one transaction per provider may hold a given reference.
CREATE UNIQUE INDEX payment_transactions_provider_ref_idx
    ON payment_transactions (provider, provider_ref)
    WHERE provider_ref IS NOT NULL AND provider_ref <> '';

-- The receipt is the provider's own settlement identifier (e.g. the M-Pesa
-- MpesaReceiptNumber); it is stamped exactly once, with the outcome.
CREATE UNIQUE INDEX payment_transactions_provider_receipt_idx
    ON payment_transactions (provider, provider_receipt)
    WHERE provider_receipt IS NOT NULL AND provider_receipt <> '';

CREATE INDEX payment_transactions_intent_idx ON payment_transactions (intent_id, created_at);

CREATE FUNCTION guard_payment_transaction_update() RETURNS trigger AS $$
BEGIN
    IF NEW.intent_id <> OLD.intent_id
        OR NEW.amount_minor <> OLD.amount_minor
        OR NEW.currency <> OLD.currency
        OR NEW.provider <> OLD.provider
        OR NEW.created_at <> OLD.created_at THEN
        RAISE EXCEPTION 'payment_transactions: ledger columns are immutable';
    END IF;
    IF OLD.status = 'PENDING' AND NEW.status IN ('SUCCEEDED','FAILED') THEN
        RETURN NEW;
    END IF;
    RAISE EXCEPTION 'payment_transactions: status % -> % is not a legal ledger move', OLD.status, NEW.status;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER payment_transactions_guarded_update
    BEFORE UPDATE ON payment_transactions
    FOR EACH ROW EXECUTE FUNCTION guard_payment_transaction_update();

CREATE FUNCTION prevent_payment_transaction_delete() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'payment_transactions is an append-only ledger: rows are never deleted';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER payment_transactions_no_delete
    BEFORE DELETE ON payment_transactions
    FOR EACH ROW EXECUTE FUNCTION prevent_payment_transaction_delete();
