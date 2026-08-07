-- Mirror of the up: restore the green/yellow color codes and their CHECK.
-- Drop the semantic-name CHECK first so the reverse rewrite is accepted.
ALTER TABLE automation DROP CONSTRAINT IF EXISTS automation_tier_check;

ALTER TABLE automation ALTER COLUMN tier SET DEFAULT 'green';

DO $$
DECLARE ws uuid;
BEGIN
  FOR ws IN SELECT id FROM workspace LOOP
    PERFORM set_config('app.workspace_id', ws::text, true);
    UPDATE automation SET tier = 'green'
    WHERE (tier = 'auto_execute')
      AND automation.workspace_id = ws;

    UPDATE automation SET tier = 'yellow'
    WHERE (tier = 'confirmation_required')
      AND automation.workspace_id = ws;
  END LOOP;
END $$;

ALTER TABLE automation
  ADD CONSTRAINT automation_tier_check CHECK (tier IN ('green', 'yellow'));
