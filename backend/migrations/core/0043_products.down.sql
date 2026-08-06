DO $$
DECLARE ws uuid;
BEGIN
  FOR ws IN SELECT id FROM workspace LOOP
    PERFORM set_config('app.workspace_id', ws::text, true);
    UPDATE role SET permissions = permissions #- '{objects,product}' WHERE is_system;
  END LOOP;
END $$;

DROP TABLE IF EXISTS product;