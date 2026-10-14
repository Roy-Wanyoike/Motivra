DROP TRIGGER IF EXISTS payment_transactions_no_delete ON payment_transactions;
DROP FUNCTION IF EXISTS prevent_payment_transaction_delete();
DROP TRIGGER IF EXISTS payment_transactions_guarded_update ON payment_transactions;
DROP FUNCTION IF EXISTS guard_payment_transaction_update();
DROP INDEX IF EXISTS payment_transactions_intent_idx;
DROP INDEX IF EXISTS payment_transactions_provider_receipt_idx;
DROP INDEX IF EXISTS payment_transactions_provider_ref_idx;
DROP TABLE IF EXISTS payment_transactions;
