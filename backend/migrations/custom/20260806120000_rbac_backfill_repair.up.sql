-- Re-apply every fork-owned RBAC backfill that row-level security may have
-- discarded, for databases that already recorded the migration carrying it.
-- The core namespace's counterpart is 0192_rbac_backfill_repair; this is the
-- same repair for the objects the fork owns (ADR-0017 custom namespace).
--
-- Scope, and why it is every guarded backfill rather than the ones observed to
-- have been lost, is argued in full at the head of core 0192. The short form:
-- which backfills an installation lost depends on where in the sequence it
-- first booted, no migration can know that, and each block is guarded on key
-- ABSENCE, so re-applying one that already landed writes nothing.
--
-- The bodies are the originals: same objects, same roles, same payloads.
-- TestTheRepairsCoverEveryGuardedRBACBackfill derives both sides from the tree
-- and fails if they ever disagree.
DO $$
DECLARE ws uuid;
BEGIN
  FOR ws IN SELECT id FROM workspace LOOP
    PERFORM set_config('app.workspace_id', ws::text, true);

    -- 20260716130000_overlay_connection_rbac
    UPDATE role SET permissions = jsonb_set(
      permissions, '{objects,overlay_connection}',
      '{"create":true,"read":true,"update":true,"delete":true}'::jsonb)
    WHERE (is_system AND key IN ('admin','ops')
      AND NOT permissions->'objects' ? 'overlay_connection')
      AND role.workspace_id = ws;
    UPDATE role SET permissions = jsonb_set(
      permissions, '{objects,overlay_connection}',
      '{"create":false,"read":true,"update":false,"delete":false}'::jsonb)
    WHERE (is_system AND key IN ('manager','rep','read_only')
      AND NOT permissions->'objects' ? 'overlay_connection')
      AND role.workspace_id = ws;

    -- 20260730130000_import_run_rbac
    UPDATE role SET permissions = jsonb_set(
      permissions, '{objects,import_run}',
      '{"create":true,"read":true,"update":true,"delete":true}'::jsonb)
    WHERE (is_system AND key IN ('admin','ops')
      AND NOT permissions->'objects' ? 'import_run')
      AND role.workspace_id = ws;
  END LOOP;
END $$;
