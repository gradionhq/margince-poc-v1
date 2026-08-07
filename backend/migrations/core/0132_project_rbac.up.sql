-- 0128: backfill the `project` RBAC object into the seeded system-role
-- policy documents of EXISTING workspaces (new workspaces get it from the
-- code-side seed, identity/internal/policy). Shipping a new first-class
-- object without this backfill is how it "works on a fresh database and
-- 403s everywhere else".
--
-- Posture mirrors `deal`, because a project IS a record people work, not
-- config: admin/manager/ops get full CRUD, a rep creates and works one but
-- does not archive it (the deal rule — archiving a body of work ends a
-- claim other people's records point at), and read_only reads.
DO $$
DECLARE ws uuid;
BEGIN
  FOR ws IN SELECT id FROM workspace LOOP
    PERFORM set_config('app.workspace_id', ws::text, true);
    UPDATE role SET permissions = jsonb_set(
      permissions, '{objects,project}',
      '{"create":true,"read":true,"update":true,"delete":true}'::jsonb)
    WHERE (is_system AND key IN ('admin','manager','ops')
      AND NOT permissions->'objects' ? 'project')
      AND role.workspace_id = ws;

    UPDATE role SET permissions = jsonb_set(
      permissions, '{objects,project}',
      '{"create":true,"read":true,"update":true,"delete":false}'::jsonb)
    WHERE (is_system AND key = 'rep'
      AND NOT permissions->'objects' ? 'project')
      AND role.workspace_id = ws;

    UPDATE role SET permissions = jsonb_set(
      permissions, '{objects,project}',
      '{"create":false,"read":true,"update":false,"delete":false}'::jsonb)
    WHERE (is_system AND key = 'read_only'
      AND NOT permissions->'objects' ? 'project')
      AND role.workspace_id = ws;
  END LOOP;
END $$;
