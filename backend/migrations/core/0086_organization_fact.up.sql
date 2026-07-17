-- 0086: organization_fact — the ratified home for deep-read category
-- findings (founder ratification R4). One row per evidenced fact about an
-- organization, in a CLOSED category/field vocabulary: company contact
-- basics, offerings, and market signals. The 11 cold-start company fields
-- keep living in organization_profile_field; this table holds only the
-- new vocabulary. value_key is the normalized dedupe key for multi-value
-- fields (service, product, certification, partner, named_customer) and
-- '' for single-value ones, so the UNIQUE constraint reads "one row per
-- value for multi-value facts, one row total for single-value ones".
-- Tenant-local FKs are composite (workspace_id, id) so a cross-workspace
-- target is rejected by the database itself; site_read's PK is id alone,
-- so the composite target key is added here (0085 has shipped).
ALTER TABLE site_read ADD CONSTRAINT uq_site_read_ws_id UNIQUE (workspace_id, id);

CREATE TABLE organization_fact (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  organization_id uuid NOT NULL,
  category text NOT NULL CHECK (category IN ('company','offering','signal')),
  field text NOT NULL,
  value text NOT NULL,
  value_key text NOT NULL DEFAULT '',
  evidence_snippet text NOT NULL,
  source_url text NOT NULL,
  confidence real NOT NULL CHECK (confidence > 0 AND confidence <= 1),
  source text NOT NULL DEFAULT 'deepread',
  captured_by text NOT NULL,
  site_read_id uuid,
  captured_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT uq_org_fact UNIQUE (workspace_id, organization_id, category, field, value_key),
  CONSTRAINT org_fact_org_fkey FOREIGN KEY (workspace_id, organization_id) REFERENCES organization (workspace_id, id) ON DELETE CASCADE,
  -- An erased dossier only detaches the provenance link (the fact stays);
  -- SET NULL names the one nullable column so workspace_id is untouched.
  CONSTRAINT org_fact_site_read_fkey FOREIGN KEY (workspace_id, site_read_id)
    REFERENCES site_read (workspace_id, id) ON DELETE SET NULL (site_read_id),
  CONSTRAINT org_fact_field_vocab CHECK (
    (category = 'company'  AND field IN ('founded_year','employee_range','phone','contact_email')) OR
    (category = 'offering' AND field IN ('service','product')) OR
    (category = 'signal'   AND field IN ('certification','partner','named_customer'))
  ),
  -- The value_key cardinality the uq_org_fact index depends on: single-value
  -- company facts key on '' (one row per field), multi-value offering/signal
  -- facts on a non-empty normalized key (one row per distinct value). Without
  -- this a malformed write could duplicate a singleton or collapse two
  -- distinct offerings — the DB enforces it, not just the store.
  CONSTRAINT org_fact_value_key_cardinality CHECK (
    (category = 'company' AND value_key = '') OR
    (category IN ('offering','signal') AND value_key <> '')
  )
);

ALTER TABLE organization_fact ENABLE ROW LEVEL SECURITY;
ALTER TABLE organization_fact FORCE ROW LEVEL SECURITY;
CREATE POLICY organization_fact_tenant_isolation ON organization_fact
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);
