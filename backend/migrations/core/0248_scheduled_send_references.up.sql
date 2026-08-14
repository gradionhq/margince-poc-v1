-- 0248: the scheduled-send row's two references say what they mean.
--
-- 0242 declared `delivery_id` as "the comms_outbound row that owns delivery
-- truth from here on" and gave it no foreign key, so nothing but that comment
-- held the two together. And it declared `activity_id ON DELETE SET NULL` while
-- its own state-shape CHECK forbids a released row with a null activity — an
-- action the constraint guarantees can never succeed.
--
-- Neither is reachable through the product today: an activity is
-- erasure-SCRUBBED rather than hard-deleted, and a delivery is not deleted at
-- all. That is what makes this a correctness repair rather than an incident,
-- and also why it is worth doing now — a DDL-level delete during maintenance is
-- exactly the moment nobody is watching.
--
-- RESTRICT on both, because the state model already decided this: a released
-- scheduled send NAMES the activity and the delivery it produced, and a row
-- that outlived either would be a claim about a message with nothing behind it.
-- Deleting them has to be refused, not papered over with a null the CHECK will
-- reject one statement later.
ALTER TABLE scheduled_send
  DROP CONSTRAINT IF EXISTS scheduled_send_activity_id_fkey;

ALTER TABLE scheduled_send
  ADD CONSTRAINT scheduled_send_activity_id_fkey
  FOREIGN KEY (activity_id) REFERENCES activity(id) ON DELETE RESTRICT;

-- The delivery this send handed the message to. Nullable for the same reason
-- activity_id is: it exists only once the row has been released.
ALTER TABLE scheduled_send
  ADD CONSTRAINT scheduled_send_delivery_id_fkey
  FOREIGN KEY (delivery_id) REFERENCES comms_outbound(id) ON DELETE RESTRICT;
