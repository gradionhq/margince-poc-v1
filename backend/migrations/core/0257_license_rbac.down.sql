-- Reverse of 0257: removes the object from the five roles the up grants it to.
-- Naming them rather than every is_system row keeps a rollback off roles this
-- migration never touched; within those five a key the up wrote and a key the
-- bootstrap seed wrote are the same key, and down-then-up restores it either way.
--
-- Nothing else to undo: the object governs a read of state that lives in the
-- deployment file and the process, so this migration created no rows.
DO $$
DECLARE ws uuid;
BEGIN
  FOR ws IN SELECT id FROM workspace LOOP
    PERFORM set_config('app.workspace_id', ws::text, true);

    UPDATE role SET permissions = permissions #- '{objects,license}'
    WHERE (is_system AND key IN ('admin','ops','manager','rep','read_only')
      AND permissions->'objects' ? 'license')
      AND role.workspace_id = ws;
  END LOOP;
END $$;
