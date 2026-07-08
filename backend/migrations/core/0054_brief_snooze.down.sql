-- Snoozed rows cannot survive without the state: fold them back to 'new'
-- (they were hidden-but-actionable, which is what 'new' means) before the
-- vocabulary narrows.
UPDATE brief_item SET state = 'new', state_at = NULL, snoozed_until = NULL WHERE state = 'snoozed';
ALTER TABLE brief_item DROP CONSTRAINT brief_item_snooze_shape;
ALTER TABLE brief_item DROP COLUMN snoozed_until;
ALTER TABLE brief_item DROP CONSTRAINT brief_item_state_check;
ALTER TABLE brief_item ADD CONSTRAINT brief_item_state_check
  CHECK (state IN ('new','acted','dismissed'));
