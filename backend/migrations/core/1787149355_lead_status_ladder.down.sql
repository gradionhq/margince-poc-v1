SET LOCAL lock_timeout = '3s';
DROP INDEX IF EXISTS idx_lead_score;
CREATE INDEX idx_lead_score ON lead (score DESC)
  WHERE archived_at IS NULL AND status IN ('new','working');
DROP INDEX IF EXISTS idx_lead_qualified_deal;
ALTER TABLE lead DROP COLUMN IF EXISTS qualified_deal_id, DROP COLUMN IF EXISTS status_set_by;
ALTER TABLE lead DROP CONSTRAINT lead_status_check;
UPDATE lead SET status = 'working' WHERE status IN ('contacted','engaged');
ALTER TABLE lead ADD CONSTRAINT lead_status_check
  CHECK (status IN ('new','working','promoted','disqualified'));
