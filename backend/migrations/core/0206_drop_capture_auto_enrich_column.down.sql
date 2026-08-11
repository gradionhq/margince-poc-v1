-- Reverse of 0206: put the column back, and put the current value in it.
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
--
-- The jsonb_typeof guard is not defensive noise. `setting` carries no per-key
-- type CHECK — the catalog in Go is what constrains values, and it only sees
-- writes that go through the store — so a hand-edited or externally seeded row
-- holding a string or null would abort the cast and take the whole rollback
-- with it. A row this build cannot read falls through to the re-added default
-- instead, which is the posture a fresh install ships with.
ALTER TABLE workspace ADD COLUMN capture_auto_enrich boolean NOT NULL DEFAULT true;

UPDATE workspace w SET capture_auto_enrich = (s.value)::boolean
  FROM setting s
 WHERE s.key = 'capture.auto_enrich'
   AND jsonb_typeof(s.value) = 'boolean'
   AND w.archived_at IS NULL;
