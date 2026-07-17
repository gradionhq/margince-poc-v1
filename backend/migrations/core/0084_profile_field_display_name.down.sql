-- Reverse 0084.
DELETE FROM organization_profile_field WHERE field = 'display_name';
ALTER TABLE organization_profile_field DROP CONSTRAINT organization_profile_field_field_check;
ALTER TABLE organization_profile_field
  ADD CONSTRAINT organization_profile_field_field_check
  CHECK (field IN ('icp','buying_center','value_proposition','usp','buying_intents',
                   'legal_name','registered_address','register_vat','industry','history'));
