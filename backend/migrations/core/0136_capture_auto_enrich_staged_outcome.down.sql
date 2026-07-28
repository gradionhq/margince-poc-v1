-- Narrow the outcome set back. A cursor recorded as 'staged' becomes 'applied':
-- both mean the same thing to the sweep (terminal, do not re-enqueue), and
-- leaving the value in place would make the restored CHECK unaddable.
UPDATE capture_auto_enrich_state SET last_outcome = 'applied' WHERE last_outcome = 'staged';

ALTER TABLE capture_auto_enrich_state
  DROP CONSTRAINT capture_auto_enrich_state_last_outcome_check;

ALTER TABLE capture_auto_enrich_state
  ADD CONSTRAINT capture_auto_enrich_state_last_outcome_check
  CHECK (last_outcome IS NULL OR
    last_outcome IN ('queued', 'applied', 'empty', 'failed', 'exhausted'));
