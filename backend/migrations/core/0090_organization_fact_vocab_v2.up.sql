-- 0090: organization_fact vocabulary v2 — the extraction taxonomy adds two
-- founder-set categories of published fact (2026-07-18): company/location
-- (every office/site the company states — multi-value, keyed like the
-- offering/signal fields) and signal/technology (platforms and stacks the
-- site names). Additive: existing rows all satisfy the widened CHECKs, so
-- the constraints are swapped in place. The value_key cardinality gains a
-- location carve-out: it is the ONE multi-value company field.

ALTER TABLE organization_fact DROP CONSTRAINT org_fact_field_vocab;
ALTER TABLE organization_fact ADD CONSTRAINT org_fact_field_vocab CHECK (
  (category = 'company'  AND field IN ('founded_year','employee_range','phone','contact_email','location')) OR
  (category = 'offering' AND field IN ('service','product')) OR
  (category = 'signal'   AND field IN ('certification','partner','named_customer','technology'))
);

ALTER TABLE organization_fact DROP CONSTRAINT org_fact_value_key_cardinality;
ALTER TABLE organization_fact ADD CONSTRAINT org_fact_value_key_cardinality CHECK (
  (category = 'company' AND field <> 'location' AND value_key = '') OR
  (category = 'company' AND field = 'location' AND value_key <> '') OR
  (category IN ('offering','signal') AND value_key <> '')
);
