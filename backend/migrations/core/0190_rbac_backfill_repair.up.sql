-- Re-apply the one core backfill that row-level security discarded, for
-- databases that already recorded 0154 as applied.
--
-- An applied version never runs again, so correcting 0154 in place fixes only
-- installations migrated from scratch afterwards. This migration is what reaches
-- the ones already deployed: same grants, same guards, in the workspace-scoped
-- shape that actually lands them.
--
-- Only channel_connection needs it. The other core RBAC objects were verified
-- present on the deployed installation — every backfill before 0148 ran while
-- the role table was still empty (roles are seeded by app code at bootstrap,
-- not by a migration), so those objects came from the code-side seed and no
-- write was lost. 0154 is the only core object whose backfill ran against role
-- rows that already existed. import_run, the other loss, is fork-owned and
-- repaired in the custom namespace.
--
-- Guarded on key absence, so this is a no-op wherever 0154 did land — including
-- every fresh database, which gets these grants from the seed.
DO $$
DECLARE ws uuid;
BEGIN
  FOR ws IN SELECT id FROM workspace LOOP
    PERFORM set_config('app.workspace_id', ws::text, true);

    UPDATE role SET permissions = jsonb_set(
      permissions, '{objects,channel_connection}',
      '{"create":true,"read":true,"update":true,"delete":true}'::jsonb)
    WHERE (is_system AND key IN ('admin','ops')
      AND NOT permissions->'objects' ? 'channel_connection')
      AND role.workspace_id = ws;

    UPDATE role SET permissions = jsonb_set(
      permissions, '{objects,channel_connection}',
      '{"create":false,"read":true,"update":false,"delete":false}'::jsonb)
    WHERE (is_system AND key IN ('manager','rep','read_only')
      AND NOT permissions->'objects' ? 'channel_connection')
      AND role.workspace_id = ws;
  END LOOP;
END $$;
