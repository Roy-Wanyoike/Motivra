DROP TRIGGER IF EXISTS vehicle_service_history_append_only ON vehicle_service_history;
DROP FUNCTION IF EXISTS prevent_vehicle_service_history_mutation();
DROP TABLE IF EXISTS vehicle_service_history;
