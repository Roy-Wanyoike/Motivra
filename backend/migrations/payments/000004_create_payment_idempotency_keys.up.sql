-- Payment idempotency keys make webhook processing at-most-once (issue
-- #62): the first delivery claims its key in the same transaction that
-- applies the ledger move; every replay of the same payload finds the key
-- taken, carries the stored payload hash for tamper evidence, and reads
-- the recorded outcome instead of applying anything.
--
-- The table is append-only up to the single result write: the key, scope
-- and payload hash are immutable, and result may be filled exactly once.
CREATE TABLE payment_idempotency_keys (
    key          text PRIMARY KEY,
    scope        text   NOT NULL CHECK (scope IN ('webhook','reconcile')),
    payload_hash text   NOT NULL CHECK (payload_hash ~ '^[0-9a-f]{64}$'),
    result       text   NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz NULL
);

CREATE INDEX payment_idempotency_keys_created_idx ON payment_idempotency_keys (created_at);

CREATE FUNCTION guard_payment_idempotency_key_update() RETURNS trigger AS $$
BEGIN
    IF NEW.key <> OLD.key
        OR NEW.scope <> OLD.scope
        OR NEW.payload_hash <> OLD.payload_hash
        OR NEW.created_at <> OLD.created_at THEN
        RAISE EXCEPTION 'payment_idempotency_keys: key identity is immutable';
    END IF;
    IF OLD.result = '' AND NEW.result <> '' THEN
        -- The one and only result write.
        RETURN NEW;
    END IF;
    RAISE EXCEPTION 'payment_idempotency_keys: result may be written exactly once';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER payment_idempotency_keys_guarded_update
    BEFORE UPDATE ON payment_idempotency_keys
    FOR EACH ROW EXECUTE FUNCTION guard_payment_idempotency_key_update();

CREATE FUNCTION prevent_payment_idempotency_key_delete() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'payment_idempotency_keys is an append-only audit table: rows are never deleted';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER payment_idempotency_keys_no_delete
    BEFORE DELETE ON payment_idempotency_keys
    FOR EACH ROW EXECUTE FUNCTION prevent_payment_idempotency_key_delete();
