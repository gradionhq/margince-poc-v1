-- Restore 0131's original vocabulary: a project may hold 'owner' again.
--
-- The down is the widening half of the one-way door the up describes. It
-- restores the CHECK and nothing else — it cannot restore rows, because the up
-- proved there were none to lose.
--
-- Reverting this alone leaves the schema able to hold a state the READ PATH
-- does not enforce: platform/auth's ownerPrivateTables does not list project,
-- so an 'owner' project would read as workspace-visible to every seat. If you
-- are reverting in order to add capture privacy to projects for real, add
-- project to ownerPrivateTables in the same change.

SET LOCAL lock_timeout = '3s';

ALTER TABLE project DROP CONSTRAINT IF EXISTS project_visibility_check;
ALTER TABLE project ADD CONSTRAINT project_visibility_check
  CHECK (visibility IN ('workspace','owner'));
