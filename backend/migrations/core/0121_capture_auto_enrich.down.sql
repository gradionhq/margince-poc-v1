-- Mirror of the up: the up wrote capture_settings to every system role, so the
-- down removes it from every system role, and drops the column.
UPDATE role SET permissions = permissions #- '{objects,capture_settings}'
  WHERE is_system AND key IN ('admin', 'ops', 'manager', 'rep', 'read_only');

ALTER TABLE workspace DROP COLUMN capture_auto_enrich;
