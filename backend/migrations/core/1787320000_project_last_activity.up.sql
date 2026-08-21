-- project.last_activity_at joins the maintained clocks (PROJ-FORM-6).
--
-- 0131 declared the column, indexed a sort on it and documented it as
-- "maintained on link write". Nothing ever wrote it: every project's clock has
-- been NULL since the table shipped, so the "Last activity" sort on the project
-- list ordered by nothing. Person, organization and deal got their clocks from
-- 1787032690's triggers; project was left out of that migration. This is the
-- missing arm, on the same mechanism rather than a second one.
--
-- A project's clock is the newest occurred_at of a live activity linked
-- DIRECTLY to the project — the same shape as the person and deal clocks, and
-- deliberately NOT the union with the activities on the project's deals. Two
-- reasons. The value stays cheap: one seek on idx_alink_project, on a write
-- path that runs a recompute per activity_link row. And it stays unambiguous:
-- "last activity on this project" answers what was filed against the project
-- itself. A reader who wants the wider story has the project timeline, which
-- offers the union separately; the account clock takes the reaching form
-- because an account with no direct activity of its own is the normal case,
-- while a project without one is a project nobody has filed against.
--
-- WHY THIS IS ADDITIVE RATHER THAN AN EDIT OF 1787032690'S FUNCTIONS. The
-- obvious shape — widen `refresh_last_activity_for_link` to take the project
-- id, add a `project` arm to `move_last_activity`, and re-create the two
-- trigger functions to match — cannot be applied by a migration role that does
-- not OWN those functions, and `CREATE OR REPLACE` needs ownership exactly as
-- `DROP` does. That is not a hypothetical: the down migration below runs
-- whenever an installation reverts, and it runs as whichever role reverted. Any
-- function the down re-created is then owned by that role, and the next
-- forward migration — run by the ordinary migration role — fails on it with
-- "must be owner of function". Widening also forces a second arity, and
-- Postgres overloads by arity, so a caller left on the three-argument form
-- keeps resolving and silently skips projects.
--
-- Everything below is therefore NEW: its own derivation, its own mover, its own
-- two triggers alongside the existing ones. Nothing 1787032690 created is
-- replaced, dropped, or re-signed, so ownership never enters into it and no
-- second arity of anything exists. The cost is one extra trigger per table,
-- which is the same recompute the shared path would have done inline.
--
-- The mechanism itself is 1787032690's, unchanged: the clock is kept by
-- triggers on the writes that move it (activity_link insert, update or delete;
-- an activity re-dated or archived), each maintenance is a recompute from the
-- timeline rather than an increment, and moving a clock is not an edit of the
-- record — the UPDATE runs under the transaction-local
-- `margince.last_activity_move` flag, so set_updated_at_bump_version leaves
-- updated_at and version alone and an editor's If-Match still holds.
--
-- The backfill below rewrites project rows and the index build locks the
-- table, so the wait is bounded rather than left to queue behind whatever
-- transaction happens to be open: a migration that stalls every write to
-- `project` indefinitely is worse than one that fails and is re-run.
SET LOCAL lock_timeout = '3s';

CREATE OR REPLACE FUNCTION last_activity_of_project(pid uuid) RETURNS timestamptz
LANGUAGE sql STABLE AS $$
  SELECT max(a.occurred_at)
    FROM activity_link l
    JOIN activity a ON a.id = l.activity_id AND a.archived_at IS NULL
   WHERE l.project_id = pid
$$;

-- The project's own mover, mirroring move_last_activity's contract for the one
-- record it covers. Lock first, then derive: under READ COMMITTED a writer that
-- waited on another's row lock re-checks its WHERE against a fresh snapshot but
-- does not re-evaluate its SET, so a recompute folded into the UPDATE would
-- store the value derived before the wait — the older one, over the newer one
-- the first writer just stored. Locking first makes the derivation run after
-- the wait, so the last writer stores the true max.
--
-- The flag is cleared explicitly rather than left to transaction end: the same
-- transaction usually goes on to write real edits that MUST bump.
CREATE OR REPLACE FUNCTION move_project_last_activity(pid uuid) RETURNS void
LANGUAGE plpgsql AS $$
DECLARE
  v timestamptz;
BEGIN
  IF pid IS NULL THEN RETURN; END IF;
  PERFORM 1 FROM project WHERE id = pid FOR UPDATE;
  v := last_activity_of_project(pid);
  PERFORM set_config('margince.last_activity_move', 'on', true);
  UPDATE project SET last_activity_at = v WHERE id = pid;
  PERFORM set_config('margince.last_activity_move', 'off', true);
END;
$$;

-- A link written, moved or removed. Both OLD and NEW are refreshed on an
-- UPDATE, so relinking an activity from one project to another moves the clock
-- it left as well as the one it joined.
CREATE OR REPLACE FUNCTION trg_activity_link_project_last_activity() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP IN ('DELETE', 'UPDATE') THEN
    PERFORM move_project_last_activity(OLD.project_id);
  END IF;
  IF TG_OP IN ('INSERT', 'UPDATE') THEN
    PERFORM move_project_last_activity(NEW.project_id);
  END IF;
  RETURN NULL;
END;
$$;

-- Dropped first rather than CREATE OR REPLACE TRIGGER: that syntax arrived in
-- PostgreSQL 14 and the rest of this tree's triggers are written the portable
-- way (0214). It also makes the whole migration re-runnable, which every other
-- statement here already is.
DROP TRIGGER IF EXISTS activity_link_project_last_activity ON activity_link;
CREATE TRIGGER activity_link_project_last_activity
  AFTER INSERT OR UPDATE OR DELETE ON activity_link
  FOR EACH ROW EXECUTE FUNCTION trg_activity_link_project_last_activity();

-- Re-dating or archiving an activity moves the clock of the project it is
-- filed against. uq_activity_link_project allows at most one project link per
-- activity, so this refreshes at most one project.
CREATE OR REPLACE FUNCTION trg_activity_project_last_activity() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  PERFORM move_project_last_activity(l.project_id)
     FROM activity_link l WHERE l.activity_id = NEW.id AND l.project_id IS NOT NULL;
  RETURN NULL;
END;
$$;

DROP TRIGGER IF EXISTS activity_project_last_activity ON activity;
CREATE TRIGGER activity_project_last_activity
  AFTER UPDATE OF occurred_at, archived_at ON activity
  FOR EACH ROW
  WHEN (OLD.occurred_at IS DISTINCT FROM NEW.occurred_at OR OLD.archived_at IS DISTINCT FROM NEW.archived_at)
  EXECUTE FUNCTION trg_activity_project_last_activity();

-- Backfill: every project's clock from the timeline as it stands. Under the
-- flag, so a backfill of a column nobody has ever read does not invalidate
-- every open editor's If-Match version.
--
-- IS DISTINCT FROM rather than a bare assignment: a project with no linked
-- activity derives NULL, which is what the column already holds, and rewriting
-- it would take a row lock held to commit for no change at all. On a fresh
-- install that is every row.
SELECT set_config('margince.last_activity_move', 'on', true);
UPDATE project SET last_activity_at = last_activity_of_project(id)
 WHERE last_activity_at IS DISTINCT FROM last_activity_of_project(id);
SELECT set_config('margince.last_activity_move', 'off', true);

-- The sort index in the ORDER BY's own shape: the sort column, then the keyset
-- tie-breakers, partial on the live rows, descending only.
CREATE INDEX IF NOT EXISTS idx_project_last_activity_keyset
  ON project (last_activity_at DESC NULLS LAST, created_at DESC, id DESC)
  WHERE archived_at IS NULL;
