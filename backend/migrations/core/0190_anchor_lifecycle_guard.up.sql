-- ADR-0082/A127: the installation's own company cannot be archived or merged.
--
-- 0083 already enforces "at most one live anchor" as a partial unique index.
-- The other half — that the one the installation was set up with stays — was
-- enforced nowhere, and losing it is not a small fault: the company read
-- resolves only a LIVE anchor, so an archived one makes the whole workspace
-- read as never configured and the application returns it to onboarding. An
-- ordinary merge did that silently.
--
-- In the schema rather than only in a service, for the same reason the
-- uniqueness half is: one missed writer reopens it, and the failure is total.

-- An installation that already lost its anchor carries the evidence: a row
-- still flagged is_anchor that is archived or merged away. The flag is dead
-- weight there — anchorOrganization resolves only a LIVE anchor, so such a row
-- has answered nothing since the day it was retired, and the partial unique
-- index does not count it either. Clearing it changes no behaviour and lets the
-- constraint below describe the rule going forward instead of failing on
-- history nobody can now repair.
UPDATE organization
   SET is_anchor = false
 WHERE is_anchor AND (archived_at IS NOT NULL OR merged_into_id IS NOT NULL);

ALTER TABLE organization
  DROP CONSTRAINT IF EXISTS organization_anchor_is_permanent;
ALTER TABLE organization
  ADD CONSTRAINT organization_anchor_is_permanent
  CHECK (NOT is_anchor OR (archived_at IS NULL AND merged_into_id IS NULL));

-- The merge TARGET needs a trigger rather than a CHECK: folding a customer into
-- the anchor leaves the anchor's own row untouched, so no constraint on it ever
-- fires — the damage is on the other side, where a customer's people, deals and
-- history are relinked onto the installation's own company and cannot be told
-- apart afterwards.
CREATE OR REPLACE FUNCTION organization_refuse_merge_into_anchor() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.merged_into_id IS NOT NULL
     AND EXISTS (SELECT 1 FROM organization o
                  WHERE o.id = NEW.merged_into_id AND o.is_anchor) THEN
    RAISE EXCEPTION 'organization % may not be merged into the anchor organization', NEW.id
      USING ERRCODE = 'check_violation',
            CONSTRAINT = 'organization_anchor_is_permanent';
  END IF;
  RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS organization_refuse_merge_into_anchor ON organization;
CREATE TRIGGER organization_refuse_merge_into_anchor
  BEFORE INSERT OR UPDATE OF merged_into_id ON organization
  FOR EACH ROW EXECUTE FUNCTION organization_refuse_merge_into_anchor();
