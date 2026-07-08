-- 0062: honest automation run history (A72 / ADR-0035 Am.1). 'blocked'
-- records a 🟡 firing whose staged approval expired or was rejected —
-- today that outcome vanished, and a run history that hides its failures
-- is worse than none. The reason rides the existing `error` column
-- (B-E15.3a: failed/blocked/skipped runs all say why).
ALTER TABLE workflow_run DROP CONSTRAINT workflow_run_status_check;
ALTER TABLE workflow_run ADD CONSTRAINT workflow_run_status_check
  CHECK (status IN ('applied','skipped','failed','requires_approval','blocked'));
