DO $$
DECLARE ws uuid;
BEGIN
  FOR ws IN SELECT id FROM workspace LOOP
    PERFORM set_config('app.workspace_id', ws::text, true);
    DELETE FROM organization_profile_field
    WHERE (source = 'migration' AND captured_by = 'system:migration-0105')
      AND organization_profile_field.workspace_id = ws;
  END LOOP;
END $$;

ALTER TABLE ai_call
  DROP COLUMN context_tokens_estimate,
  DROP COLUMN context_bytes;

ALTER TABLE site_read DROP COLUMN first_grounded_at;
