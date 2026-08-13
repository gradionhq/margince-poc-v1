-- Dropped in dependency order: the claim table and the reservations reference
-- the run, the run and the budget reference the connection.
DROP TABLE IF EXISTS person_provider_claim;
DROP TABLE IF EXISTS provider_run_reservation;
DROP TABLE IF EXISTS provider_run;
DROP TABLE IF EXISTS provider_connection_budget;
DROP TABLE IF EXISTS provider_connection;
