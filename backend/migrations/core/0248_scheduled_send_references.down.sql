ALTER TABLE scheduled_send
  DROP CONSTRAINT IF EXISTS scheduled_send_delivery_id_fkey;

ALTER TABLE scheduled_send
  DROP CONSTRAINT IF EXISTS scheduled_send_activity_id_fkey;

ALTER TABLE scheduled_send
  ADD CONSTRAINT scheduled_send_activity_id_fkey
  FOREIGN KEY (activity_id) REFERENCES activity(id) ON DELETE SET NULL;
