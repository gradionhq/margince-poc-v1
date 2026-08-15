ALTER TABLE scheduled_send DROP CONSTRAINT IF EXISTS scheduled_send_agent_provenance_shape;
ALTER TABLE scheduled_send
  DROP COLUMN IF EXISTS agent_on_behalf_of,
  DROP COLUMN IF EXISTS agent_passport_id,
  DROP COLUMN IF EXISTS agent_actor_id;
