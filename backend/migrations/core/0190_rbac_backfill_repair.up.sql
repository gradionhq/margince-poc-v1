-- Re-apply the one core backfill that row-level security discarded, for
-- databases that already recorded 0154 as applied.
--
-- An applied version never runs again, so correcting 0154 in place fixes only
-- installations migrated from scratch afterwards. This migration is what reaches
-- the ones already deployed: same grants, same guards, in the workspace-scoped
-- shape that actually lands them.
--
-- channel_connection is the only core RBAC object that needs it, and the reason
-- is structural rather than observed: roles are seeded by app code at bootstrap,
-- never by a migration, so a backfill that runs before an installation's first
-- boot has no rows to write and nothing to lose. Only an object introduced AFTER
-- an installation exists can lose its backfill, and channel_connection is the
-- only core one that did. import_run is the other, fork-owned, and repaired in
-- the custom namespace.
--
-- The RBAC losses are the ones this repairs. Whether a non-RBAC backfill was also
-- lost depends on whether an installation held matching rows when it ran, which
-- no migration can answer for every installation — issue #541 tracks the audit.
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
