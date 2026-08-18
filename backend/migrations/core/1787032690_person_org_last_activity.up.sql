-- last_activity_at on person and organization (PO-DDL-1/-4 as amended
-- 2026-08-18): when something last happened with the record, kept current on
-- the write exactly as deal.last_activity_at is meant to be — a read
-- accelerator the timeline can always rebuild, never a second truth. It backs
-- the "Last activity" sort on both lists (DM-VOCAB-1/2).
--
-- A person's clock is the newest occurred_at of a live activity linked to
-- them. An account's clock is the newest occurred_at of a live activity that
-- REACHES it — filed against it, against one of its deals, or against a
-- contact it currently employs — the same three arms activities.OrgReachSet
-- walks for the account timeline, so the column and the timeline agree.
--
-- The clocks are maintained HERE, by triggers on the writes that move them,
-- rather than at a call site in Go. The activity_link row is written by more
-- than one module (activities logs, capture files, relink moves), and the
-- reach set changes without any activity being written at all — an employment
-- starts or ends, a deal moves account, an activity is archived or re-dated.
-- A clock kept at one call site is stale on every other path; a clock kept on
-- the row moves for every writer of that row. The writes covered are exactly:
-- activity_link insert/update/delete, activity occurred_at or archived_at
-- change, employment insert/update/delete, and a deal changing account. Not
-- covered, and not covered by the timeline either: a person merge does not
-- re-point activity_link at the survivor (people/ensure.go), so a merged-away
-- contact's history stays on the loser's clock as it stays on the loser's
-- timeline. Each maintenance is a recompute from the timeline (max over live
-- linked activities), never an increment, so it is idempotent and converges
-- under concurrent writers.
--
-- The deal clock joins the same mechanism: activities.insertActivityLinks used
-- to advance it in Go, on the logging path only, and capture never did.
--
-- Moving a clock is NOT an edit of the record: set_updated_at_bump_version
-- skips an UPDATE whose only change is last_activity_at, so an arriving mail
-- neither rewrites updated_at nor invalidates an editor's If-Match version.

ALTER TABLE person       ADD COLUMN IF NOT EXISTS last_activity_at timestamptz NULL;
ALTER TABLE organization ADD COLUMN IF NOT EXISTS last_activity_at timestamptz NULL;

-- The clock refresh below runs its UPDATEs under a transaction-local flag; the
-- bump trigger sees it and leaves updated_at and version alone. A flag rather
-- than a row comparison because a BEFORE trigger's NEW does not yet carry the
-- STORED generated columns (search_tsv), so OLD and NEW never compare equal.
CREATE OR REPLACE FUNCTION set_updated_at_bump_version() RETURNS trigger AS $$
BEGIN
  IF current_setting('margince.last_activity_move', true) = 'on' THEN
    RETURN NEW;
  END IF;
  NEW.updated_at = now();
  NEW.version = OLD.version + 1;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- Every clock UPDATE goes through this, so the flag is set and cleared in one
-- place. Cleared explicitly rather than left to transaction end: the same
-- transaction usually goes on to write real edits that MUST bump.
CREATE OR REPLACE FUNCTION move_last_activity(tbl regclass, rid uuid) RETURNS void
LANGUAGE plpgsql AS $$
BEGIN
  IF rid IS NULL THEN RETURN; END IF;
  PERFORM set_config('margince.last_activity_move', 'on', true);
  CASE tbl
    WHEN 'person'::regclass THEN
      UPDATE person SET last_activity_at = last_activity_of_person(rid) WHERE id = rid;
    WHEN 'deal'::regclass THEN
      UPDATE deal SET last_activity_at = last_activity_of_deal(rid) WHERE id = rid;
    WHEN 'organization'::regclass THEN
      UPDATE organization SET last_activity_at = last_activity_of_organization(rid) WHERE id = rid;
  END CASE;
  PERFORM set_config('margince.last_activity_move', 'off', true);
END;
$$;

-- The three clock derivations, from the timeline's own tables.
CREATE OR REPLACE FUNCTION last_activity_of_person(pid uuid) RETURNS timestamptz
LANGUAGE sql STABLE AS $$
  SELECT max(a.occurred_at)
    FROM activity_link l
    JOIN activity a ON a.id = l.activity_id AND a.archived_at IS NULL
   WHERE l.person_id = pid
$$;

CREATE OR REPLACE FUNCTION last_activity_of_deal(did uuid) RETURNS timestamptz
LANGUAGE sql STABLE AS $$
  SELECT max(a.occurred_at)
    FROM activity_link l
    JOIN activity a ON a.id = l.activity_id AND a.archived_at IS NULL
   WHERE l.deal_id = did
$$;

CREATE OR REPLACE FUNCTION last_activity_of_organization(oid uuid) RETURNS timestamptz
LANGUAGE sql STABLE AS $$
  SELECT max(a.occurred_at)
    FROM activity_link l
    LEFT JOIN deal d ON d.id = l.deal_id
    LEFT JOIN relationship r ON r.person_id = l.person_id AND r.kind = 'employment'
      AND r.ended_at IS NULL AND r.archived_at IS NULL
    JOIN activity a ON a.id = l.activity_id AND a.archived_at IS NULL
   WHERE oid IN (l.organization_id, d.organization_id, r.organization_id)
$$;

-- Recompute the clocks a link touches: its person, its deal, and every account
-- it reaches (directly, through the deal, through the person's live employers).
CREATE OR REPLACE FUNCTION refresh_last_activity_for_link(pid uuid, did uuid, oid uuid) RETURNS void
LANGUAGE plpgsql AS $$
DECLARE
  reached uuid;
BEGIN
  PERFORM move_last_activity('person', pid);
  PERFORM move_last_activity('deal', did);
  FOR reached IN
     SELECT oid WHERE oid IS NOT NULL
     UNION SELECT d.organization_id FROM deal d WHERE d.id = did AND d.organization_id IS NOT NULL
     UNION SELECT r.organization_id FROM relationship r
            WHERE r.person_id = pid AND r.kind = 'employment' AND r.ended_at IS NULL AND r.archived_at IS NULL
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

CREATE TRIGGER activity_link_last_activity
  AFTER INSERT OR UPDATE OR DELETE ON activity_link
  FOR EACH ROW EXECUTE FUNCTION trg_activity_link_last_activity();

-- Re-dating or archiving an activity moves every clock it counted toward.
CREATE OR REPLACE FUNCTION trg_activity_last_activity() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  PERFORM refresh_last_activity_for_link(l.person_id, l.deal_id, l.organization_id)
     FROM activity_link l WHERE l.activity_id = NEW.id;
  RETURN NULL;
END;
$$;

CREATE TRIGGER activity_last_activity
  AFTER UPDATE OF occurred_at, archived_at ON activity
  FOR EACH ROW
  WHEN (OLD.occurred_at IS DISTINCT FROM NEW.occurred_at OR OLD.archived_at IS DISTINCT FROM NEW.archived_at)
  EXECUTE FUNCTION trg_activity_last_activity();

-- An employment starting, ending, moving or being archived changes which
-- account a contact's activities reach.
CREATE OR REPLACE FUNCTION trg_relationship_last_activity() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP IN ('DELETE', 'UPDATE') AND OLD.kind = 'employment' THEN
    PERFORM move_last_activity('organization', OLD.organization_id);
  END IF;
  IF TG_OP IN ('INSERT', 'UPDATE') AND NEW.kind = 'employment' THEN
    PERFORM move_last_activity('organization', NEW.organization_id);
  END IF;
  RETURN NULL;
END;
$$;

CREATE TRIGGER relationship_last_activity
  AFTER INSERT OR UPDATE OR DELETE ON relationship
  FOR EACH ROW EXECUTE FUNCTION trg_relationship_last_activity();

-- A deal moving to another account takes its activities' reach with it.
CREATE OR REPLACE FUNCTION trg_deal_last_activity() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  PERFORM move_last_activity('organization', OLD.organization_id);
  PERFORM move_last_activity('organization', NEW.organization_id);
  RETURN NULL;
END;
$$;

CREATE TRIGGER deal_last_activity
  AFTER UPDATE OF organization_id ON deal
  FOR EACH ROW
  WHEN (OLD.organization_id IS DISTINCT FROM NEW.organization_id)
  EXECUTE FUNCTION trg_deal_last_activity();

-- Backfill: every clock from the timeline as it stands. Deal included, since
-- capture never advanced it.
SELECT set_config('margince.last_activity_move', 'on', true);
UPDATE person       SET last_activity_at = last_activity_of_person(id);
UPDATE deal         SET last_activity_at = last_activity_of_deal(id);
UPDATE organization SET last_activity_at = last_activity_of_organization(id);
SELECT set_config('margince.last_activity_move', 'off', true);

-- Sort indexes in the ORDER BY's own shape (see 0292): the sort column, then
-- the keyset tie-breakers, partial on the live rows, descending only — recency
-- is asked for newest-first.
CREATE INDEX IF NOT EXISTS idx_person_last_activity_keyset
  ON person (last_activity_at DESC NULLS LAST, created_at DESC, id DESC)
  WHERE archived_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_org_last_activity_keyset
  ON organization (last_activity_at DESC NULLS LAST, created_at DESC, id DESC)
  WHERE archived_at IS NULL;
