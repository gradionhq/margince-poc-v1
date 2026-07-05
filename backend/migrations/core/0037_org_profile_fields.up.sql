-- 0037: the accepted cold-start profile substrate (features/07 §1).
-- Seven of the ten read-back fields (icp, buying_center,
-- value_proposition, usp, buying_intents, register_vat, history) have no
-- organization column and no home anywhere in data-model.md — filed as
-- feedback/16; this table is the build-side substrate until the spec
-- ratifies one. One row per (organization, field), carrying the verbatim
-- evidence so an accepted value stays visually distinguishable from a
-- human-typed one and auditable back to its page.
CREATE TABLE organization_profile_field (
  id              uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id    uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  organization_id uuid NOT NULL,
  field           text NOT NULL CHECK (field IN ('icp','buying_center','value_proposition','usp','buying_intents','legal_name','registered_address','register_vat','industry','history')),
  value           text NOT NULL,
  evidence_snippet text NOT NULL,
  source_url      text NOT NULL,
  confidence      real NOT NULL CHECK (confidence > 0 AND confidence <= 1),
  source          text NOT NULL DEFAULT 'coldstart',
  captured_by     text NOT NULL,
  captured_at     timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT uq_org_profile_field UNIQUE (workspace_id, organization_id, field),
  CONSTRAINT org_profile_field_org_fkey FOREIGN KEY (workspace_id, organization_id)
    REFERENCES organization (workspace_id, id) ON DELETE CASCADE
);

ALTER TABLE organization_profile_field ENABLE ROW LEVEL SECURITY;
ALTER TABLE organization_profile_field FORCE ROW LEVEL SECURITY;
CREATE POLICY organization_profile_field_tenant_isolation ON organization_profile_field
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);
