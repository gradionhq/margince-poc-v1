-- Reverse 0083.
DROP INDEX IF EXISTS uq_organization_anchor;
ALTER TABLE organization DROP COLUMN IF EXISTS is_anchor;
