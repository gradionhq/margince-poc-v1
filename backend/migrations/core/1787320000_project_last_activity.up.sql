-- project.last_activity_at joins the maintained clocks (PROJ-FORM-6).
--
-- 0131 declared the column, indexed a sort on it and documented it as
-- "maintained on link write". Nothing ever wrote it: every project's clock has
-- been NULL since the table shipped, so the "Last activity" sort on the project
-- list ordered by nothing. Person, organization and deal got their clocks from
-- 1787032690's triggers; project was left out of that migration. This is the
-- missing arm, built on the same mechanism rather than a second one.
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
-- Everything else is the mechanism 1787032690 already established: the clock is
-- kept by triggers on the writes that move it (activity_link insert, update or
-- delete; an activity re-dated or archived), each maintenance is a recompute
-- from the timeline rather than an increment, and moving a clock is not an edit
-- of the record — the UPDATE runs under the transaction-local
-- `margince.last_activity_move` flag, so set_updated_at_bump_version leaves
-- updated_at and version alone and an editor's If-Match still holds.
--
-- The backfill below rewrites every project row and the index build locks the
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

-- The project arm of the shared mover. Lock first, then derive: under READ
-- COMMITTED a writer that waited on another's row lock re-checks its WHERE
-- against a fresh snapshot but does not re-evaluate its SET, so a recompute
-- folded into the UPDATE would store the value derived before the wait.
CREATE OR REPLACE FUNCTION move_last_activity(tbl regclass, rid uuid) RETURNS void
LANGUAGE plpgsql AS $$
DECLARE
  v timestamptz;
BEGIN
  IF rid IS NULL THEN RETURN; END IF;
  CASE tbl
    WHEN 'person'::regclass THEN
      PERFORM 1 FROM person WHERE id = rid FOR UPDATE;
      v := last_activity_of_person(rid);
      PERFORM set_config('margince.last_activity_move', 'on', true);
      UPDATE person SET last_activity_at = v WHERE id = rid;
    WHEN 'deal'::regclass THEN
      PERFORM 1 FROM deal WHERE id = rid FOR UPDATE;
      v := last_activity_of_deal(rid);
      PERFORM set_config('margince.last_activity_move', 'on', true);
      UPDATE deal SET last_activity_at = v WHERE id = rid;
    WHEN 'organization'::regclass THEN
      PERFORM 1 FROM organization WHERE id = rid FOR UPDATE;
      v := last_activity_of_organization(rid);
      PERFORM set_config('margince.last_activity_move', 'on', true);
      UPDATE organization SET last_activity_at = v WHERE id = rid;
    WHEN 'project'::regclass THEN
      PERFORM 1 FROM project WHERE id = rid FOR UPDATE;
      v := last_activity_of_project(rid);
      PERFORM set_config('margince.last_activity_move', 'on', true);
      UPDATE project SET last_activity_at = v WHERE id = rid;
  END CASE;
  PERFORM set_config('margince.last_activity_move', 'off', true);
END;
$$;

-- refresh_last_activity_for_link now carries the project id too. Postgres
-- overloads by arity, so the four-argument form does NOT replace the
-- three-argument one: both callers below are re-created in this migration so
-- they resolve to the new form, and the old overload is dropped afterwards so
-- no future caller can bind the arity that ignores projects.
CREATE OR REPLACE FUNCTION refresh_last_activity_for_link(pid uuid, did uuid, oid uuid, prj uuid) RETURNS void
LANGUAGE plpgsql AS $$
DECLARE
  reached uuid;
BEGIN
  PERFORM move_last_activity('person', pid);
  PERFORM move_last_activity('deal', did);
  PERFORM move_last_activity('project', prj);
  -- Ordered by id: two writers reaching the same accounts lock them in the
  -- same order, so they queue rather than deadlock.
  FOR reached IN
     SELECT x FROM (
       SELECT oid AS x WHERE oid IS NOT NULL
       UNION SELECT d.organization_id FROM deal d WHERE d.id = did AND d.organization_id IS NOT NULL
       UNION SELECT r.organization_id FROM relationship r
              WHERE r.person_id = pid AND r.kind = 'employment' AND r.ended_at IS NULL AND r.archived_at IS NULL
     ) reach ORDER BY x
  LOOP
    PERFORM move_last_activity('organization', reached);
  END LOOP;
END;
$$;

CREATE OR REPLACE FUNCTION trg_activity_link_last_activity() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP IN ('DELETE', 'UPDATE') THEN
    PERFORM refresh_last_activity_for_link(OLD.person_id, OLD.deal_id, OLD.organization_id, OLD.project_id);
  END IF;
  IF TG_OP IN ('INSERT', 'UPDATE') THEN
    PERFORM refresh_last_activity_for_link(NEW.person_id, NEW.deal_id, NEW.organization_id, NEW.project_id);
  END IF;
  RETURN NULL;
END;
$$;

CREATE OR REPLACE FUNCTION trg_activity_last_activity() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  PERFORM refresh_last_activity_for_link(l.person_id, l.deal_id, l.organization_id, l.project_id)
     FROM activity_link l WHERE l.activity_id = NEW.id;
  RETURN NULL;
END;
$$;

DROP FUNCTION IF EXISTS refresh_last_activity_for_link(uuid, uuid, uuid);

-- Backfill: every project's clock from the timeline as it stands. Under the
-- flag, so a backfill of a column nobody has ever read does not invalidate
-- every open editor's If-Match version.
SELECT set_config('margince.last_activity_move', 'on', true);
UPDATE project SET last_activity_at = last_activity_of_project(id);
SELECT set_config('margince.last_activity_move', 'off', true);

-- The sort index in the ORDER BY's own shape: the sort column, then the keyset
-- tie-breakers, partial on the live rows, descending only.
CREATE INDEX IF NOT EXISTS idx_project_last_activity_keyset
  ON project (last_activity_at DESC NULLS LAST, created_at DESC, id DESC)
  WHERE archived_at IS NULL;
