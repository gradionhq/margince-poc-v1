-- Reverse: projects stop being maintained and the shared mover goes back to
-- the three records 1787032690 gave it. project.last_activity_at is 0131's
-- column, so it STAYS, keeping whatever the up migration last computed —
-- nothing maintains it again after this, exactly as it stood before.
--
-- Dropping the sort index locks `project`; bound the wait rather than queue
-- behind whatever transaction is open.
SET LOCAL lock_timeout = '3s';

-- The three-argument overload comes back, and both callers are re-created to
-- bind it, before the four-argument form is dropped out from under them.
CREATE OR REPLACE FUNCTION refresh_last_activity_for_link(pid uuid, did uuid, oid uuid) RETURNS void
LANGUAGE plpgsql AS $$
DECLARE
  reached uuid;
BEGIN
  PERFORM move_last_activity('person', pid);
  PERFORM move_last_activity('deal', did);
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
    PERFORM refresh_last_activity_for_link(OLD.person_id, OLD.deal_id, OLD.organization_id);
  END IF;
  IF TG_OP IN ('INSERT', 'UPDATE') THEN
    PERFORM refresh_last_activity_for_link(NEW.person_id, NEW.deal_id, NEW.organization_id);
  END IF;
  RETURN NULL;
END;
$$;

CREATE OR REPLACE FUNCTION trg_activity_last_activity() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  PERFORM refresh_last_activity_for_link(l.person_id, l.deal_id, l.organization_id)
     FROM activity_link l WHERE l.activity_id = NEW.id;
  RETURN NULL;
END;
$$;

DROP FUNCTION IF EXISTS refresh_last_activity_for_link(uuid, uuid, uuid, uuid);

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
  END CASE;
  PERFORM set_config('margince.last_activity_move', 'off', true);
END;
$$;

DROP FUNCTION IF EXISTS last_activity_of_project(uuid);
DROP INDEX IF EXISTS idx_project_last_activity_keyset;
