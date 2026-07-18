-- 0090 down: restore the v1 vocabulary CHECKs. Rows using the v2 fields
-- (company/location, signal/technology) must be removed first or the
-- narrowed constraints refuse to attach — the guard DELETE makes the down
-- migration honest instead of leaving it to fail midway.
DELETE FROM organization_fact WHERE field IN ('location','technology');

ALTER TABLE organization_fact DROP CONSTRAINT org_fact_field_vocab;
ALTER TABLE organization_fact ADD CONSTRAINT org_fact_field_vocab CHECK (
  (category = 'company'  AND field IN ('founded_year','employee_range','phone','contact_email')) OR
  (category = 'offering' AND field IN ('service','product')) OR
  (category = 'signal'   AND field IN ('certification','partner','named_customer'))
);

ALTER TABLE organization_fact DROP CONSTRAINT org_fact_value_key_cardinality;
ALTER TABLE organization_fact ADD CONSTRAINT org_fact_value_key_cardinality CHECK (
  (category = 'company' AND value_key = '') OR
  (category IN ('offering','signal') AND value_key <> '')
);
