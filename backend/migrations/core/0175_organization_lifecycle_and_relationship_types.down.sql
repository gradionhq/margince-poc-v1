-- Reverse of 0175. classification was never dropped and never stopped being
-- written by the migration's own backfill, so the down path only has to remove
-- what 0175 added: the type rows go with their table, and lifecycle goes with
-- its column. Nothing needs to be reconstructed.

DROP TABLE IF EXISTS organization_relationship_type;

DROP INDEX IF EXISTS idx_org_lifecycle;

ALTER TABLE organization DROP COLUMN IF EXISTS lifecycle;

COMMENT ON COLUMN organization.classification IS NULL;
