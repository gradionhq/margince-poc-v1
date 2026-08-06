-- 20260730130000: backfill the `import_run` RBAC object into the seeded
-- system-role policy documents of EXISTING workspaces (new workspaces get
-- it from the code-side seed, identity/internal/policy). Fork-owned
-- migration (ADR-0017 custom namespace) — import_run is the migration
-- module's own object.
--
-- Posture mirrors the overlay_connection backfill
-- (20260716130000_overlay_connection_rbac.up.sql): a migration run is a
-- workspace-wide bulk mutation of the estate (the flip's importer runs
-- through it), so every verb is admin/ops-only; other roles hold no
-- grant at all — a rep neither starts nor reads migration runs.
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
