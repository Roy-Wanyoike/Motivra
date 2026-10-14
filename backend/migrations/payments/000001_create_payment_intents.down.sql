DROP TRIGGER IF EXISTS payment_intents_integrity ON payment_intents;
DROP FUNCTION IF EXISTS prevent_payment_intent_terminal_mutation();
DROP INDEX IF EXISTS payment_intents_status_idx;
DROP INDEX IF EXISTS payment_intents_created_by_idx;
DROP INDEX IF EXISTS payment_intents_customer_idx;
DROP INDEX IF EXISTS payment_intents_tenant_created_idx;
DROP TABLE IF EXISTS payment_intents;
