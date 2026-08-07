-- Removes the object from the two roles the up grants it to, and only those.
-- Stripping every system role would erase the key a freshly seeded installation
-- writes for manager/rep/read_only — the up never touched those, so a rollback
-- has no business removing them.
DO $$
DECLARE ws uuid;
BEGIN
  FOR ws IN SELECT id FROM workspace LOOP
    PERFORM set_config('app.workspace_id', ws::text, true);
    UPDATE role SET permissions = permissions #- '{objects,import_run}'
    WHERE (is_system AND key IN ('admin','ops')
      AND permissions->'objects' ? 'import_run')
      AND role.workspace_id = ws;
  END LOOP;
END $$;
