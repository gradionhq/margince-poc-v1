SET LOCAL lock_timeout = '3s';
DROP INDEX IF EXISTS idx_lead_disqualify_reason;
ALTER TABLE lead DROP COLUMN IF EXISTS disqualify_reason_id, DROP COLUMN IF EXISTS disqualify_note;
DROP TABLE IF EXISTS lead_disqualify_reason;
DROP TABLE IF EXISTS lead_source;
