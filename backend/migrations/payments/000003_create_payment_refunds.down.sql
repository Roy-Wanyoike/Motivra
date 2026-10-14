DROP TRIGGER IF EXISTS payment_refunds_no_delete ON payment_refunds;
DROP FUNCTION IF EXISTS prevent_payment_refund_delete();
DROP TRIGGER IF EXISTS payment_refunds_guarded_update ON payment_refunds;
DROP FUNCTION IF EXISTS guard_payment_refund_update();
DROP INDEX IF EXISTS payment_refunds_intent_idx;
DROP INDEX IF EXISTS payment_refunds_one_in_flight_idx;
DROP TABLE IF EXISTS payment_refunds;
