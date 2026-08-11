-- Mirror of the up: the seeded system roles return to the pre-0210 posture,
-- where no role held `create` on capture_settings.
DO $$
DECLARE ws uuid;
BEGIN
  FOR ws IN SELECT id FROM workspace LOOP
    PERFORM set_config('app.workspace_id', ws::text, true);

    UPDATE role SET permissions = jsonb_set(
      permissions, '{objects,capture_settings,create}', 'false'::jsonb)
    WHERE is_system AND key IN ('admin','ops','manager','rep')
      AND permissions->'objects' ? 'capture_settings'
      AND role.workspace_id = ws;
  END LOOP;
END $$;
