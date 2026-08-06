-- Reverse 0084.
DO $$
DECLARE ws uuid;
BEGIN
  FOR ws IN SELECT id FROM workspace LOOP
    PERFORM set_config('app.workspace_id', ws::text, true);
    DELETE FROM organization_profile_field WHERE field = 'display_name';
  END LOOP;
END $$;

ALTER TABLE organization_profile_field DROP CONSTRAINT organization_profile_field_field_check;
ALTER TABLE organization_profile_field
  ADD CONSTRAINT organization_profile_field_field_check
  CHECK (field IN ('icp','buying_center','value_proposition','usp','buying_intents',
                   'legal_name','registered_address','register_vat','industry','history')) NOT VALID;
ALTER TABLE organization_profile_field VALIDATE CONSTRAINT organization_profile_field_field_check;