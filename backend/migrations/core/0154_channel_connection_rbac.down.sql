-- Mirror of the up: the up wrote the object into all five system roles (and
-- only where absent), so the down removes it from those five. Naming them
-- rather than every is_system row is what keeps a rollback off the roles this
-- migration never touched.
--
-- Within those five it cannot be finer. A key the up wrote and a key the
-- bootstrap seed wrote are the same key — the document carries no provenance —
-- so a rollback removes either. That is the honest cost of reversibility here,
-- and it is recoverable: the up is guarded on absence and writes the same
-- payload, so down-then-up restores what the seed would have.
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
