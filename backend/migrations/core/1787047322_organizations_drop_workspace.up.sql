-- 1787047322: the organization cluster drops the tenant column (ADR-0091 §8 phase D).
--
-- The company record and the five tables that describe one:
--
--   organization                     the account itself
--   organization_domain              the addresses that identify it
--   organization_domain_disposition  what capture decided about a domain
--   organization_fact                what a deep read established, by category
--   organization_profile_field       a claimed field with its evidence
--   organization_relationship_type   how we stand to it — customer, partner
--
-- Every key here already names something narrower than the tenant, and phase B
-- put them there: a domain is unique installation-wide, the anchor company is a
-- `UNIQUE ((true))` singleton, a fact is unique by
-- (organization_id, category, field, value_key).
--
-- uq_organization_ws_id goes with the column — phase B's leftover, a second copy
-- of organization's own primary key, referenced by no foreign key and indexing
-- nothing organization_pkey does not.

ALTER TABLE organization_relationship_type DROP CONSTRAINT organization_relationship_type_workspace_id_fkey;
ALTER TABLE organization_relationship_type DROP COLUMN workspace_id;

ALTER TABLE organization_profile_field DROP CONSTRAINT organization_profile_field_workspace_id_fkey;
ALTER TABLE organization_profile_field DROP COLUMN workspace_id;

ALTER TABLE organization_fact DROP CONSTRAINT organization_fact_workspace_id_fkey;
ALTER TABLE organization_fact DROP COLUMN workspace_id;

ALTER TABLE organization_domain_disposition DROP CONSTRAINT organization_domain_disposition_workspace_id_fkey;
ALTER TABLE organization_domain_disposition DROP COLUMN workspace_id;

ALTER TABLE organization_domain DROP CONSTRAINT organization_domain_workspace_id_fkey;
ALTER TABLE organization_domain DROP COLUMN workspace_id;

ALTER TABLE organization DROP CONSTRAINT uq_organization_ws_id;
ALTER TABLE organization DROP CONSTRAINT organization_workspace_id_fkey;
ALTER TABLE organization DROP COLUMN workspace_id;

-- The indexes that led with the column, recreated on what actually selects
-- rows: a classification, a lifecycle stage, an owner, a company's relationship
-- types, a disposition's moment.
--
-- idx_org_ws_live is NOT recreated: it was (workspace_id) WHERE archived_at IS
-- NULL, and an index on the tenant alone has no narrowed form.
CREATE INDEX idx_org_class ON organization (classification) WHERE archived_at IS NULL;
CREATE INDEX idx_org_lifecycle ON organization (lifecycle) WHERE archived_at IS NULL;
CREATE INDEX idx_org_owner ON organization (owner_id) WHERE archived_at IS NULL;
CREATE INDEX idx_org_rel_type_cascade ON organization_relationship_type (organization_id);
CREATE INDEX idx_org_rel_type_org ON organization_relationship_type (organization_id) WHERE archived_at IS NULL;
CREATE INDEX idx_domain_disposition_admission ON organization_domain_disposition (admission_at DESC) WHERE admission IS NOT NULL;
CREATE INDEX idx_domain_disposition_unevidenced ON organization_domain_disposition (updated_at DESC) WHERE pending_reason = 'unevidenced';
