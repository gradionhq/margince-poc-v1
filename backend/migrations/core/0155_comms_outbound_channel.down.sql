-- 0149 rollback: the mail-only shape cannot represent a channel delivery, so
-- the channel-shaped rows are dropped first (the 0147 precedent). Reversing
-- this migration therefore forgets every channel send it recorded — a
-- re-applied 0149 starts with mail deliveries only.
DO $$
DECLARE ws uuid;
BEGIN
  FOR ws IN SELECT id FROM workspace LOOP
    PERFORM set_config('app.workspace_id', ws::text, true);
    DELETE FROM comms_outbound WHERE channel_user_id IS NOT NULL;
  END LOOP;
END $$;


ALTER TABLE comms_outbound DROP CONSTRAINT comms_outbound_shape;

ALTER TABLE comms_outbound
  DROP COLUMN channel_user_id,
  ALTER COLUMN message_id SET NOT NULL,
  ALTER COLUMN recipients SET NOT NULL,
  ALTER COLUMN cc SET NOT NULL,
  ALTER COLUMN subject SET NOT NULL,
  ALTER COLUMN references_chain SET NOT NULL;