-- 0084: admit display_name into the read-back vocabulary. The extraction can
-- now quote what a company calls itself day to day (the landing page states it
-- where the register entry does not), and the form's most obvious field stops
-- being the one the read-back can never fill. Evidence rows land here like
-- every other read-back field; organization.display_name itself is NOT NULL
-- and human-owned, so no column fill applies.
ALTER TABLE organization_profile_field DROP CONSTRAINT organization_profile_field_field_check;
ALTER TABLE organization_profile_field
  ADD CONSTRAINT organization_profile_field_field_check
  CHECK (field IN ('icp','buying_center','value_proposition','usp','buying_intents',
                   'legal_name','registered_address','register_vat','industry','history',
                   'display_name'));
