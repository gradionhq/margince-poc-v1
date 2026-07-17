DROP INDEX IF EXISTS idx_capture_connection_account;
ALTER TABLE capture_connection DROP COLUMN IF EXISTS account_email;
