-- Every seat may contribute a consumer-mail domain (CAP-PARAM-5): the seeded
-- system roles admin/ops/manager/rep gain `create` on capture_settings. The
-- capture store demands `create` for inserting a NEW `extra` entry, while
-- `never` carve-outs, kind overwrites and removal keep demanding `update`
-- (admin/ops). read_only stays read-only. Mirrors the policy.go defaults so a
-- deployed database and a fresh install agree.
DO $$
DECLARE ws uuid;
BEGIN
  FOR ws IN SELECT id FROM workspace LOOP
    PERFORM set_config('app.workspace_id', ws::text, true);

    UPDATE role SET permissions = jsonb_set(
      permissions, '{objects,capture_settings,create}', 'true'::jsonb)
    WHERE is_system AND key IN ('admin','ops','manager','rep')
      AND permissions->'objects' ? 'capture_settings'
      AND role.workspace_id = ws;
  END LOOP;
END $$;
