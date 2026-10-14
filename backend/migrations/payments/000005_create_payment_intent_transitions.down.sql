DROP TRIGGER IF EXISTS payment_intent_transitions_append_only ON payment_intent_transitions;
DROP FUNCTION IF EXISTS prevent_payment_intent_transitions_mutation();
DROP INDEX IF EXISTS payment_intent_transitions_intent_idx;
DROP TABLE IF EXISTS payment_intent_transitions;
