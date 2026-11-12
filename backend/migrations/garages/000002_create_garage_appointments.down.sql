-- Reversal of 000002: the appointments table had no dependents inside the
-- garages chain (audit_log belongs to the identity chain and outlives the
-- audit rows that referenced appointments). The btree_gist extension is
-- cluster-shared state that other chains may depend on; it is deliberately
-- NOT dropped here.
DROP TABLE IF EXISTS garage_appointments;
