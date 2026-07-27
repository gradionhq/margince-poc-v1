-- Mirror of the up: the up wrote the object into all five system roles
-- (and only where absent), so the down removes it from those five. Scoping
-- the removal to the roles the up wrote keeps rollback from erasing a
-- project grant this migration did not create.
UPDATE role SET permissions = permissions #- '{objects,project}'
  WHERE is_system AND key IN ('admin','manager','ops','rep','read_only');
