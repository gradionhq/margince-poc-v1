DO $$
DECLARE ws uuid;
BEGIN
  FOR ws IN SELECT id FROM workspace LOOP
    PERFORM set_config('app.workspace_id', ws::text, true);
    UPDATE site_read SET status = 'failed'
    WHERE (status = 'cancelled')
      AND site_read.workspace_id = ws;
  END LOOP;
END $$;

ALTER TABLE site_read DROP CONSTRAINT site_read_status_check;
ALTER TABLE site_read ADD CONSTRAINT site_read_status_check
  CHECK (status IN ('queued', 'deferred', 'running', 'done', 'partial', 'failed'));
