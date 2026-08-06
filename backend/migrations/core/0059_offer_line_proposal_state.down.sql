-- Staged (never-accepted) lines cannot survive a world without the flag:
-- they were invisible to totals and must not silently start counting.
DO $$
DECLARE ws uuid;
BEGIN
  FOR ws IN SELECT id FROM workspace LOOP
    PERFORM set_config('app.workspace_id', ws::text, true);
    DELETE FROM offer_line_item WHERE proposal_state = 'staged';
  END LOOP;
END $$;

ALTER TABLE offer_line_item DROP COLUMN proposal_state;