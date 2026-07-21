-- 0099: ADR-0065 company-context vocabulary and provenance.
-- Single curated statements stay in organization_profile_field; repeatable
-- offerings, markets and proof stay in organization_fact. Existing rows are
-- normalized to the source vocabulary exposed by the company-context wire.

ALTER TABLE organization_profile_field
  DROP CONSTRAINT organization_profile_field_field_check;
ALTER TABLE organization_profile_field
  ADD CONSTRAINT organization_profile_field_field_check CHECK (field IN (
    'display_name','offer_summary','icp','value_proposition','usp',
    'customer_pains','desired_outcomes','buying_center','buying_intents',
    'common_objections','sales_motion','legal_name','registered_address',
    'register_vat','industry','history'
  ));

UPDATE organization_profile_field
SET source = CASE source
  WHEN 'manual' THEN 'human'
  WHEN 'coldstart' THEN 'site_read'
  WHEN 'deepread' THEN 'site_read'
  WHEN 'enrich' THEN 'connector'
  ELSE source
END;
ALTER TABLE organization_profile_field ALTER COLUMN source SET DEFAULT 'site_read';
ALTER TABLE organization_profile_field
  ADD CONSTRAINT organization_profile_field_source_check
  CHECK (source IN ('human','site_read','connector','migration'));
ALTER TABLE organization_profile_field
  DROP CONSTRAINT organization_profile_field_confidence_check;
ALTER TABLE organization_profile_field
  ALTER COLUMN evidence_snippet DROP NOT NULL,
  ALTER COLUMN source_url DROP NOT NULL,
  ALTER COLUMN confidence DROP NOT NULL,
  ADD COLUMN version bigint NOT NULL DEFAULT 1,
  ADD COLUMN updated_at timestamptz NOT NULL DEFAULT now(),
  ADD CONSTRAINT organization_profile_field_confidence_check
    CHECK (confidence IS NULL OR confidence BETWEEN 0 AND 1),
  ADD CONSTRAINT org_profile_site_evidence CHECK (
    source <> 'site_read' OR
    (evidence_snippet IS NOT NULL AND evidence_snippet <> '' AND
     source_url IS NOT NULL AND source_url <> '' AND confidence IS NOT NULL)
  );
CREATE TRIGGER trg_organization_profile_field_updated
  BEFORE UPDATE ON organization_profile_field
  FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();

ALTER TABLE organization_fact DROP CONSTRAINT organization_fact_category_check;
ALTER TABLE organization_fact
  ADD CONSTRAINT organization_fact_category_check
  CHECK (category IN ('company','offering','market','signal'));

ALTER TABLE organization_fact DROP CONSTRAINT org_fact_field_vocab;
ALTER TABLE organization_fact ADD CONSTRAINT org_fact_field_vocab CHECK (
  (category = 'company' AND field IN ('founded_year','employee_range','phone','contact_email','location')) OR
  (category = 'offering' AND field IN ('service','product','capability')) OR
  (category = 'market' AND field IN ('served_industry','company_size','geography','language')) OR
  (category = 'signal' AND field IN ('certification','partner','named_customer','technology','quantified_outcome'))
);

ALTER TABLE organization_fact DROP CONSTRAINT org_fact_value_key_cardinality;
ALTER TABLE organization_fact ADD CONSTRAINT org_fact_value_key_cardinality CHECK (
  (category = 'company' AND field <> 'location' AND value_key = '') OR
  (category = 'company' AND field = 'location' AND value_key <> '') OR
  (category IN ('offering','market','signal') AND value_key <> '')
);

UPDATE organization_fact
SET source = CASE source
  WHEN 'manual' THEN 'human'
  WHEN 'coldstart' THEN 'site_read'
  WHEN 'deepread' THEN 'site_read'
  WHEN 'enrich' THEN 'connector'
  ELSE source
END;
ALTER TABLE organization_fact ALTER COLUMN source SET DEFAULT 'site_read';
ALTER TABLE organization_fact
  ADD CONSTRAINT organization_fact_source_check
  CHECK (source IN ('human','site_read','connector','migration'));
ALTER TABLE organization_fact
  DROP CONSTRAINT organization_fact_confidence_check;
ALTER TABLE organization_fact
  ALTER COLUMN evidence_snippet DROP NOT NULL,
  ALTER COLUMN source_url DROP NOT NULL,
  ALTER COLUMN confidence DROP NOT NULL,
  ADD COLUMN version bigint NOT NULL DEFAULT 1,
  ADD COLUMN updated_at timestamptz NOT NULL DEFAULT now(),
  ADD CONSTRAINT organization_fact_confidence_check
    CHECK (confidence IS NULL OR confidence BETWEEN 0 AND 1),
  ADD CONSTRAINT org_fact_site_evidence CHECK (
    source <> 'site_read' OR
    (evidence_snippet IS NOT NULL AND evidence_snippet <> '' AND
     source_url IS NOT NULL AND source_url <> '' AND confidence IS NOT NULL)
  );
CREATE TRIGGER trg_organization_fact_updated
  BEFORE UPDATE ON organization_fact
  FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();
