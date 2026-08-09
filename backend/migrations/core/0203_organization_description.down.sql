-- Reverse of 0203. The column drop takes its constraint with it.
--
-- No DELETE runs here, so the migration role's lack of BYPASSRLS is not a
-- factor: this down migration removes structure only.
ALTER TABLE organization
  DROP COLUMN IF EXISTS description;
