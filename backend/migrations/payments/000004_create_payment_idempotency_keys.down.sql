DROP TRIGGER IF EXISTS payment_idempotency_keys_no_delete ON payment_idempotency_keys;
DROP FUNCTION IF EXISTS prevent_payment_idempotency_key_delete();
DROP TRIGGER IF EXISTS payment_idempotency_keys_guarded_update ON payment_idempotency_keys;
DROP FUNCTION IF EXISTS guard_payment_idempotency_key_update();
DROP INDEX IF EXISTS payment_idempotency_keys_created_idx;
DROP TABLE IF EXISTS payment_idempotency_keys;
