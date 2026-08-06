-- Reverse of 0191. Column drops take their constraints and indexes with them.
--
-- No DELETE runs here, so the migration role's lack of BYPASSRLS is not a
-- factor: this down migration removes structure only.

ALTER TABLE organization_fact
  DROP CONSTRAINT IF EXISTS org_fact_verified_pair;
ALTER TABLE organization_profile_field
  DROP CONSTRAINT IF EXISTS org_profile_field_verified_pair;

ALTER TABLE organization_fact
  DROP COLUMN IF EXISTS verified_by,
  DROP COLUMN IF EXISTS verified_at,
  DROP COLUMN IF EXISTS retrieved_at;

ALTER TABLE organization_profile_field
  DROP COLUMN IF EXISTS verified_by,
  DROP COLUMN IF EXISTS verified_at,
  DROP COLUMN IF EXISTS retrieved_at;

DROP INDEX IF EXISTS organization_linkedin_url_key;
ALTER TABLE organization
  DROP CONSTRAINT IF EXISTS organization_linkedin_url_shape;
ALTER TABLE organization
  DROP COLUMN IF EXISTS linkedin_url;
