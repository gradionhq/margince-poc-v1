-- Reverses 0190. The anchor becomes archivable and mergeable again; nothing is
-- rewritten, because the guard only ever refused writes rather than changing
-- rows.

DROP TRIGGER IF EXISTS organization_refuse_merge_into_anchor ON organization;
DROP FUNCTION IF EXISTS organization_refuse_merge_into_anchor();

ALTER TABLE organization
  DROP CONSTRAINT IF EXISTS organization_anchor_is_permanent;
