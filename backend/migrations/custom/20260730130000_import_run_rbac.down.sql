DO $$
DECLARE ws uuid;
BEGIN
  FOR ws IN SELECT id FROM workspace LOOP
    PERFORM set_config('app.workspace_id', ws::text, true);
    UPDATE role SET permissions = permissions #- '{objects,import_run}'
    WHERE (is_system AND permissions->'objects' ? 'import_run')
      AND role.workspace_id = ws;
  END LOOP;
END $$;
