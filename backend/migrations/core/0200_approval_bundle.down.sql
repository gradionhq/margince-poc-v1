DROP INDEX IF EXISTS idx_approval_bundle;
ALTER TABLE approval DROP COLUMN IF EXISTS bundle_id;
