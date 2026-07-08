-- A blocked run is a failed firing with a recorded reason; 'failed' is
-- the honest nearest value in the narrower vocabulary.
UPDATE workflow_run SET status = 'failed' WHERE status = 'blocked';
ALTER TABLE workflow_run DROP CONSTRAINT workflow_run_status_check;
ALTER TABLE workflow_run ADD CONSTRAINT workflow_run_status_check
  CHECK (status IN ('applied','skipped','failed','requires_approval'));
