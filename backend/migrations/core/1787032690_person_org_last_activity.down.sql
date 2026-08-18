-- Reverse: the clocks stop being maintained and the two new columns go; the
-- timeline they were derived from is untouched. deal.last_activity_at (0006)
-- stays and KEEPS the values the up migration recomputed from the live
-- timeline — a recompute is the better residue than the monotone high-water
-- mark the removed Go writer left, and nothing maintains it again after this.
DROP TRIGGER IF EXISTS deal_last_activity ON deal;
DROP TRIGGER IF EXISTS relationship_last_activity ON relationship;
DROP TRIGGER IF EXISTS activity_last_activity ON activity;
DROP TRIGGER IF EXISTS activity_link_last_activity ON activity_link;
DROP FUNCTION IF EXISTS trg_deal_last_activity();
DROP FUNCTION IF EXISTS trg_relationship_last_activity();
DROP FUNCTION IF EXISTS trg_activity_last_activity();
DROP FUNCTION IF EXISTS trg_activity_link_last_activity();
DROP FUNCTION IF EXISTS refresh_last_activity_for_link(uuid, uuid, uuid);
DROP FUNCTION IF EXISTS move_last_activity(regclass, uuid);
DROP FUNCTION IF EXISTS last_activity_of_organization(uuid);
DROP FUNCTION IF EXISTS last_activity_of_deal(uuid);
DROP FUNCTION IF EXISTS last_activity_of_person(uuid);
DROP INDEX IF EXISTS idx_org_last_activity_keyset;
DROP INDEX IF EXISTS idx_person_last_activity_keyset;
ALTER TABLE organization DROP COLUMN IF EXISTS last_activity_at;
ALTER TABLE person       DROP COLUMN IF EXISTS last_activity_at;

CREATE OR REPLACE FUNCTION set_updated_at_bump_version() RETURNS trigger AS $$
BEGIN
  NEW.updated_at = now();
  NEW.version = OLD.version + 1;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;
