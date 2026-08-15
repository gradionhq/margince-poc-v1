-- Mirror of the up: it wrote the object into all five system roles (and only
-- where absent), so this removes it from those five. Naming them rather than
-- every is_system row keeps a rollback off roles this migration never touched.
--
-- Within those five it cannot be finer: a key the up wrote and a key the
-- bootstrap seed wrote are the same key — the document carries no provenance —
-- so a rollback removes either. Recoverable, because the up is guarded on
-- absence and writes the same payload, so down-then-up restores what the seed
-- would have.
DO $$
DECLARE ws uuid;
BEGIN
  FOR ws IN SELECT id FROM workspace LOOP
    PERFORM set_config('app.workspace_id', ws::text, true);
    UPDATE role SET permissions = permissions #- '{objects,capture_trace}'
    WHERE (is_system AND key IN ('admin','manager','ops','rep','read_only'))
      AND role.workspace_id = ws;
  END LOOP;
END $$;
