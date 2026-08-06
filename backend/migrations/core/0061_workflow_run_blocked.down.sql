-- A blocked run is a failed firing with a recorded reason; 'failed' is
-- the honest nearest value in the narrower vocabulary.
DO $$
DECLARE ws uuid;
BEGIN
  FOR ws IN SELECT id FROM workspace LOOP
    PERFORM set_config('app.workspace_id', ws::text, true);
    UPDATE workflow_run SET status = 'failed' WHERE status = 'blocked';
  END LOOP;
END $$;

ALTER TABLE workflow_run DROP CONSTRAINT workflow_run_status_check;
ALTER TABLE workflow_run ADD CONSTRAINT workflow_run_status_check
  CHECK (status IN ('applied','skipped','failed','requires_approval'));