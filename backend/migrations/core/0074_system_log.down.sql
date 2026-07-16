-- Reverse 0074: drop the table (its trigger goes with it) then the
-- trigger function, so a full down leaves no orphaned function behind
-- (TestMigrations_applyReverseReapply re-applies from zero).
DROP TABLE IF EXISTS system_log;
DROP FUNCTION IF EXISTS system_log_immutable();
