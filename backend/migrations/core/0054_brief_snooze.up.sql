-- 0054: brief-item snooze (A77 / AC-home-6). A snoozed item hides until
-- `snoozed_until` passes, then re-surfaces as actionable — distinct from
-- `dismissed` (gone) and `acted` (done). `state_at` keeps carrying the
-- changed-since cursor for snoozes exactly as for the other transitions
-- (brief_item_state_stamped already covers it: snoozed ≠ new ⇒ stamped).
ALTER TABLE brief_item DROP CONSTRAINT brief_item_state_check;
ALTER TABLE brief_item ADD CONSTRAINT brief_item_state_check
  CHECK (state IN ('new','acted','dismissed','snoozed'));
ALTER TABLE brief_item ADD COLUMN snoozed_until timestamptz NULL;
ALTER TABLE brief_item ADD CONSTRAINT brief_item_snooze_shape
  CHECK ((snoozed_until IS NOT NULL) = (state = 'snoozed'));
