-- Backfill the `saved_view` RBAC object into the seeded system-role policy
-- documents of EXISTING workspaces. `saved_view` entered the code-side seed
-- (identity/internal/policy) without a backfill, so every workspace
-- bootstrapped before that release holds no saved_view grant and 403s on
-- every saved-view route — permanently, because the bootstrap seed
-- (identity.seedSystemRoles) writes the defaults once and never re-syncs.
--
-- Posture: CRUD for ALL FIVE roles, read_only included. A saved view is the
-- user's own per-user view state — a P1-exempt personal preference, not a
-- shared record — and the store scopes it to its owner, so full
-- self-service is correct even for a role that may write nothing else.
DO $$
DECLARE ws uuid;
BEGIN
  FOR ws IN SELECT id FROM workspace LOOP
    PERFORM set_config('app.workspace_id', ws::text, true);
    UPDATE role SET permissions = jsonb_set(
      permissions, '{objects,saved_view}',
      '{"create":true,"read":true,"update":true,"delete":true}'::jsonb)
    WHERE (is_system AND key IN ('admin','ops','manager','rep','read_only')
      AND NOT permissions->'objects' ? 'saved_view')
      AND role.workspace_id = ws;
  END LOOP;
END $$;
