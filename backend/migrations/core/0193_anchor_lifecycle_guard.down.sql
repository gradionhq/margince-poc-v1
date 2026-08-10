-- Reverses 0193's GUARDS. The anchor becomes archivable and mergeable again.
--
-- 0193's data repair is deliberately ONE-WAY, and this file does not pretend
-- otherwise. The up cleared is_anchor from organizations that were already
-- archived or merged away; those rows are not re-flagged here.
--
-- Restoring the flag would be the wrong answer, not merely an expensive one.
-- Such a row is retired, so the flag answered nothing while it was set: the
-- anchor resolver requires archived_at IS NULL, and 0083's unique index is
-- partial on the same condition. But un-archiving that organization after a
-- restore would put TWO live anchors in one workspace, which 0083 refuses — so
-- the restore turns a dead flag into a row that cannot be brought back at all.
--
-- Which organization it used to be is not lost: the repair is one UPDATE by the
-- migration role, and the row still carries its own history.

DROP TRIGGER IF EXISTS organization_refuse_anchor_retirement ON organization;
DROP FUNCTION IF EXISTS organization_refuse_anchor_retirement();

ALTER TABLE organization
  DROP CONSTRAINT IF EXISTS organization_anchor_is_permanent;
