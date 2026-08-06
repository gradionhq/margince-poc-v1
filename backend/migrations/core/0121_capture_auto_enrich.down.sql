-- Mirror of the up: the up wrote capture_settings to every system role, so the
-- down removes it from every system role, and drops the column.
DO $$
DECLARE ws uuid;
BEGIN
  FOR ws IN SELECT id FROM workspace LOOP
    PERFORM set_config('app.workspace_id', ws::text, true);
    UPDATE role SET permissions = permissions #- '{objects,capture_settings}'
    WHERE (is_system AND key IN ('admin', 'ops', 'manager', 'rep', 'read_only'))
      AND role.workspace_id = ws;
  END LOOP;
END $$;


ALTER TABLE workspace DROP COLUMN capture_auto_enrich;
