-- Reverse of 0194: put the column back, and put the current value in it.
--
-- The default matches 0121's, so a workspace that never had a setting row
-- lands on the posture it shipped with. The UPDATE then carries across
-- whatever the setting says, because after 0190 the row is where every change
-- since has been written — restoring the column without it would resurrect a
-- posture an operator turned off.
--
-- This has to be a two-step add-then-fill rather than a DEFAULT alone: the
-- column is NOT NULL, so it needs a value for existing rows before the
-- constraint can hold, and the value worth having is the one in `setting`.
ALTER TABLE workspace ADD COLUMN capture_auto_enrich boolean NOT NULL DEFAULT true;

UPDATE workspace w SET capture_auto_enrich = (s.value)::boolean
  FROM setting s
 WHERE s.key = 'capture.auto_enrich'
   AND w.archived_at IS NULL;
