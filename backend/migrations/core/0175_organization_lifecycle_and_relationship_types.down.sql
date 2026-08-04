-- Reverse of 0175. classification is untouched by this pair: 0175 READS it to
-- fill the new vocabulary and leaves the column standing (retired, written by
-- nothing, dropped in a follow-up). So the down path only removes what 0175
-- added — the type rows go with their table, lifecycle goes with its column —
-- and nothing has to be reconstructed, because nothing was overwritten.

DROP TABLE IF EXISTS organization_relationship_type;

DROP INDEX IF EXISTS idx_org_lifecycle;

ALTER TABLE organization DROP COLUMN IF EXISTS lifecycle;

COMMENT ON COLUMN organization.classification IS NULL;
