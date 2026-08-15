-- The installation's entitlement becomes readable through the API, behind a new
-- `license` RBAC object.
--
-- Adding an object still costs a policy backfill, exactly as 0191 and 0121 paid
-- it: the code-side seed writes each role document once at workspace creation
-- and never re-syncs, so an object added to policy.coreObjects alone works on a
-- fresh database and 403s on every installation that bootstrapped earlier.
--
-- Posture mirrors import_run and retention_policy rather than
-- installation_settings: admin/ops only, READ INCLUDED. A seat meter is the
-- installation's commercial standing, and UC-ADMIN-03 F1 gives a rep their own
-- seat and not the workspace's entitlement.
--
-- READ IS THE ONLY VERB granted to anybody, admin included. The license token is
-- resolved from the deployment file at boot, so no API write exists for a grant
-- to govern; create/update/delete stay FALSE against any future generic path.
DO $$
DECLARE ws uuid;
BEGIN
  FOR ws IN SELECT id FROM workspace LOOP
    PERFORM set_config('app.workspace_id', ws::text, true);

    UPDATE role SET permissions = jsonb_set(
      permissions, '{objects,license}',
      '{"create":false,"read":true,"update":false,"delete":false}'::jsonb)
    WHERE (is_system AND key IN ('admin','ops')
      AND NOT permissions->'objects' ? 'license')
      AND role.workspace_id = ws;

    UPDATE role SET permissions = jsonb_set(
      permissions, '{objects,license}',
      '{"create":false,"read":false,"update":false,"delete":false}'::jsonb)
    WHERE (is_system AND key IN ('manager','rep','read_only')
      AND NOT permissions->'objects' ? 'license')
      AND role.workspace_id = ws;
  END LOOP;
END $$;
