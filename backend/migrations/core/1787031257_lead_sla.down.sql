DROP INDEX IF EXISTS idx_lead_sla_open;
ALTER TABLE lead
  DROP COLUMN IF EXISTS sla_breached_at,
  DROP COLUMN IF EXISTS first_response_at,
  DROP COLUMN IF EXISTS routed_at;
