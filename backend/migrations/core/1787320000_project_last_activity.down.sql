-- Reverse: projects stop being maintained. project.last_activity_at is 0131's
-- column, so the column itself stays — but its VALUES are cleared.
--
-- 1787032690's down keeps deal.last_activity_at because a Go writer maintained
-- that column before the trigger existed, so the values survive something.
-- Nothing has ever written a project's clock, so leaving the backfill behind
-- would not restore the prior state: it would strand timestamps that no longer
-- move when an activity is archived or re-dated, and a reader cannot tell a
-- stale one from a live one. Reversing means going back to NULL.
--
-- Everything dropped here was created by the up migration, and nothing that
-- predates it is re-created. That is deliberate and load-bearing: a down runs
-- as whichever role reverted the installation, and any function it re-created
-- would then be owned by that role, so the next forward migration — run by the
-- ordinary migration role — would fail on it with "must be owner of function".
-- The up is additive for the same reason; keeping the down purely subtractive
-- is the other half of that property.
--
-- Dropping the sort index locks `project`; bound the wait rather than queue
-- behind whatever transaction is open.
SET LOCAL lock_timeout = '3s';

DROP TRIGGER IF EXISTS activity_project_last_activity ON activity;
DROP TRIGGER IF EXISTS activity_link_project_last_activity ON activity_link;
DROP FUNCTION IF EXISTS trg_activity_project_last_activity();
DROP FUNCTION IF EXISTS trg_activity_link_project_last_activity();
DROP FUNCTION IF EXISTS move_project_last_activity(uuid);
DROP FUNCTION IF EXISTS last_activity_of_project(uuid);
DROP INDEX IF EXISTS idx_project_last_activity_keyset;

-- Back to NULL, under the flag: a clock nothing maintains is worse than no
-- clock, because it reads as current. The flag keeps the reversal from bumping
-- every project's version, exactly as the backfill did on the way up.
SELECT set_config('margince.last_activity_move', 'on', true);
UPDATE project SET last_activity_at = NULL WHERE last_activity_at IS NOT NULL;
SELECT set_config('margince.last_activity_move', 'off', true);
