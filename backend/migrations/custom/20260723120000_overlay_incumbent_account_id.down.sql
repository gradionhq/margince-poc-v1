DROP INDEX IF EXISTS idx_incumbent_connection_account;
ALTER TABLE incumbent_connection DROP COLUMN IF EXISTS incumbent_account_id;
