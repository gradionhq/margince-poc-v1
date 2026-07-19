DROP TRIGGER trg_organization_fact_updated ON organization_fact;
ALTER TABLE organization_fact DROP CONSTRAINT org_fact_site_evidence;
UPDATE organization_fact
SET evidence_snippet = COALESCE(evidence_snippet, ''),
    source_url = COALESCE(source_url, ''),
    confidence = COALESCE(confidence, 1);
ALTER TABLE organization_fact
  DROP CONSTRAINT organization_fact_confidence_check,
  ALTER COLUMN evidence_snippet SET NOT NULL,
  ALTER COLUMN source_url SET NOT NULL,
  ALTER COLUMN confidence SET NOT NULL,
  ADD CONSTRAINT organization_fact_confidence_check CHECK (confidence > 0 AND confidence <= 1),
  DROP COLUMN version,
  DROP COLUMN updated_at;
ALTER TABLE organization_fact DROP CONSTRAINT organization_fact_source_check;
ALTER TABLE organization_fact ALTER COLUMN source SET DEFAULT 'deepread';
UPDATE organization_fact SET source = 'manual' WHERE source = 'human';
UPDATE organization_fact SET source = 'deepread' WHERE source = 'site_read';
UPDATE organization_fact SET source = 'enrich' WHERE source = 'connector';

DELETE FROM organization_fact
WHERE category = 'market'
   OR field IN ('capability','quantified_outcome');

ALTER TABLE organization_fact DROP CONSTRAINT org_fact_value_key_cardinality;
ALTER TABLE organization_fact ADD CONSTRAINT org_fact_value_key_cardinality CHECK (
  (category = 'company' AND field <> 'location' AND value_key = '') OR
  (category = 'company' AND field = 'location' AND value_key <> '') OR
  (category IN ('offering','signal') AND value_key <> '')
);
ALTER TABLE organization_fact DROP CONSTRAINT org_fact_field_vocab;
ALTER TABLE organization_fact ADD CONSTRAINT org_fact_field_vocab CHECK (
  (category = 'company' AND field IN ('founded_year','employee_range','phone','contact_email','location')) OR
  (category = 'offering' AND field IN ('service','product')) OR
  (category = 'signal' AND field IN ('certification','partner','named_customer','technology'))
);
ALTER TABLE organization_fact DROP CONSTRAINT organization_fact_category_check;
ALTER TABLE organization_fact
  ADD CONSTRAINT organization_fact_category_check
  CHECK (category IN ('company','offering','signal'));

ALTER TABLE organization_profile_field DROP CONSTRAINT organization_profile_field_source_check;
DROP TRIGGER trg_organization_profile_field_updated ON organization_profile_field;
ALTER TABLE organization_profile_field DROP CONSTRAINT org_profile_site_evidence;
UPDATE organization_profile_field
SET evidence_snippet = COALESCE(evidence_snippet, ''),
    source_url = COALESCE(source_url, ''),
    confidence = COALESCE(confidence, 1);
ALTER TABLE organization_profile_field
  DROP CONSTRAINT organization_profile_field_confidence_check,
  ALTER COLUMN evidence_snippet SET NOT NULL,
  ALTER COLUMN source_url SET NOT NULL,
  ALTER COLUMN confidence SET NOT NULL,
  ADD CONSTRAINT organization_profile_field_confidence_check CHECK (confidence > 0 AND confidence <= 1),
  DROP COLUMN version,
  DROP COLUMN updated_at;
ALTER TABLE organization_profile_field ALTER COLUMN source SET DEFAULT 'coldstart';
UPDATE organization_profile_field SET source = 'manual' WHERE source = 'human';
UPDATE organization_profile_field SET source = 'coldstart' WHERE source = 'site_read';
UPDATE organization_profile_field SET source = 'enrich' WHERE source = 'connector';

DELETE FROM organization_profile_field
WHERE field IN ('offer_summary','customer_pains','desired_outcomes','common_objections','sales_motion');
ALTER TABLE organization_profile_field
  DROP CONSTRAINT organization_profile_field_field_check;
ALTER TABLE organization_profile_field
  ADD CONSTRAINT organization_profile_field_field_check CHECK (field IN (
    'icp','buying_center','value_proposition','usp','buying_intents',
    'legal_name','registered_address','register_vat','industry','history','display_name'
  ));
