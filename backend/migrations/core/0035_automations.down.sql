DO $$
DECLARE ws uuid;
BEGIN
  FOR ws IN SELECT id FROM workspace LOOP
    PERFORM set_config('app.workspace_id', ws::text, true);
    UPDATE role SET permissions = permissions #- '{objects,automation}'
    WHERE is_system AND permissions->'objects' ? 'automation';
  END LOOP;
END $$;

DROP TABLE automation;