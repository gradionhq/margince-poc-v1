-- Lead first-response SLA (formulas §18): when the clock started by routing,
-- when the lead was first genuinely responded to, and when a breach was
-- escalated — the at-most-once mark for the escalation.
ALTER TABLE lead
  ADD COLUMN routed_at         timestamptz NULL,
  ADD COLUMN first_response_at timestamptz NULL,
  ADD COLUMN sla_breached_at   timestamptz NULL;

-- The breach scan's working set: open leads still owing a first response
-- that have not yet been escalated.
CREATE INDEX idx_lead_sla_open ON lead (created_at)
  WHERE archived_at IS NULL AND first_response_at IS NULL AND sla_breached_at IS NULL;
