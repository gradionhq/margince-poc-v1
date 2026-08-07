-- Mirror of the up: the up added these objects only to admin/ops (and only
-- where absent), so the down removes them only from admin/ops. Naming those two
-- rather than every is_system row is what keeps a rollback off manager, rep and
-- read_only, which this migration never touched.
--
-- Within admin/ops it cannot be finer. A key the up wrote and a key the
-- bootstrap seed wrote are the same key — the document carries no provenance —
-- so a rollback removes either. That is the honest cost of reversibility here,
-- and it is recoverable: the up is guarded on absence and writes the same
-- payload, so down-then-up restores what the seed would have.
DO $$
DECLARE ws uuid;
BEGIN
  FOR ws IN SELECT id FROM workspace LOOP
    PERFORM set_config('app.workspace_id', ws::text, true);
    UPDATE role SET permissions = permissions #- '{objects,fx_rate}'
    WHERE (is_system AND key IN ('admin','ops'))
      AND role.workspace_id = ws;

    UPDATE role SET permissions = permissions #- '{objects,ai_model_rate}'
    WHERE (is_system AND key IN ('admin','ops'))
      AND role.workspace_id = ws;
  END LOOP;
END $$;
