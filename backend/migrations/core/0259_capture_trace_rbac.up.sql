-- 0259: backfill the `capture_trace` RBAC object into EXISTING workspaces'
-- seeded role documents. New workspaces get it from the code-side seed
-- (identity/internal/policy). Shipping a new object without this backfill is how
-- one "works on a fresh database and 403s everywhere else".
--
-- READ-ONLY for everyone who holds it, and that is not a conservative default:
-- nothing writes this table but the capture pipeline and nothing deletes from it
-- but its 24-hour sweep, so there is no create/update/delete for a role to hold.
--
-- WHO holds it, and why the two who do not are the interesting half. The object
-- governs the WORKSPACE view only — rows from a shared channel binding (a
-- Telegram bot, a Zalo OA) whose inbound traffic belongs to no single member.
-- A member's own capture activity is reached by a different operation that takes
-- no grant at all, because their own traffic is their own data.
--
-- So admin, ops and manager read: debugging a shared bot binding is their work.
-- rep and read_only hold nothing rather than holding read — there is no
-- shared-channel debugging in either job, and a grant that looks harmless today
-- is the one somebody widens later without re-asking whose mail it reaches.
DO $$
DECLARE ws uuid;
BEGIN
  FOR ws IN SELECT id FROM workspace LOOP
    PERFORM set_config('app.workspace_id', ws::text, true);

    UPDATE role SET permissions = jsonb_set(
      permissions, '{objects,capture_trace}',
      '{"create":false,"read":true,"update":false,"delete":false}'::jsonb)
    WHERE (is_system AND key IN ('admin', 'ops', 'manager')
      AND NOT permissions->'objects' ? 'capture_trace')
      AND role.workspace_id = ws;

    UPDATE role SET permissions = jsonb_set(
      permissions, '{objects,capture_trace}',
      '{"create":false,"read":false,"update":false,"delete":false}'::jsonb)
    WHERE (is_system AND key IN ('rep', 'read_only')
      AND NOT permissions->'objects' ? 'capture_trace')
      AND role.workspace_id = ws;
  END LOOP;
END $$;
