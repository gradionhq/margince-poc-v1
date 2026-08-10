-- Snoozed rows cannot survive without the state: fold them back to 'new'
-- (they were hidden-but-actionable, which is what 'new' means) before the
-- vocabulary narrows.
DO $$
DECLARE ws uuid;
BEGIN
  FOR ws IN SELECT id FROM workspace LOOP
    PERFORM set_config('app.workspace_id', ws::text, true);
    UPDATE brief_item SET state = 'new', state_at = NULL, snoozed_until = NULL
    WHERE (state = 'snoozed')
      AND brief_item.workspace_id = ws;
  END LOOP;
END $$;

ALTER TABLE brief_item DROP CONSTRAINT brief_item_snooze_shape;
ALTER TABLE brief_item DROP COLUMN snoozed_until;
ALTER TABLE brief_item DROP CONSTRAINT brief_item_state_check;
ALTER TABLE brief_item ADD CONSTRAINT brief_item_state_check
  CHECK (state IN ('new','acted','dismissed'));
