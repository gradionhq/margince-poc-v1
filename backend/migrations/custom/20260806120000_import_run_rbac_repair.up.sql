-- Re-apply 20260730130000_import_run_rbac for databases that already recorded it
-- as applied, where row-level security discarded its write. Core 0190 is the
-- same repair for the same reason; this half lives here because import_run is
-- the migration module's own object and core must not write it (ADR-0017).
--
-- Without it, an admin on an already-deployed installation gets 403 on the
-- overlay->native cutover and migrate-in surfaces, which gate on `import_run`
-- (compose/flipbundle.go, compose/flipreconcile.go).
--
-- Grants admin/ops only, exactly as the original does: the other three roles
-- hold NO key, and absence is the deny. Writing them an all-false grant would
-- differ from what a freshly seeded installation stores, and the upgrade-replay
-- gate compares against that seed.
DO $$
DECLARE ws uuid;
BEGIN
  FOR ws IN SELECT id FROM workspace LOOP
    PERFORM set_config('app.workspace_id', ws::text, true);

    UPDATE role SET permissions = jsonb_set(
      permissions, '{objects,import_run}',
      '{"create":true,"read":true,"update":true,"delete":true}'::jsonb)
    WHERE (is_system AND key IN ('admin','ops')
      AND NOT permissions->'objects' ? 'import_run')
      AND role.workspace_id = ws;
  END LOOP;
END $$;
