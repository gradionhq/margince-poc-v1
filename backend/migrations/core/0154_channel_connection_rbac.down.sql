-- Mirror of the up: the up wrote the object into all five system roles
-- (and only where absent), so the down removes it from those five. Scoping
-- the removal to the roles the up wrote keeps rollback from erasing a
-- channel_connection grant this migration did not create.
DO $$
DECLARE ws uuid;
BEGIN
  FOR ws IN SELECT id FROM workspace LOOP
    PERFORM set_config('app.workspace_id', ws::text, true);
    UPDATE role SET permissions = permissions #- '{objects,channel_connection}'
    WHERE (is_system AND key IN ('admin','manager','ops','rep','read_only'))
      AND role.workspace_id = ws;
  END LOOP;
END $$;
