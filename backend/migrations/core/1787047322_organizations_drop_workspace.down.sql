-- Reverse of 1787047322: the organization cluster carries the tenant column again.
--
-- The backfill reads the LIVE workspace — archived_at IS NULL, oldest first.
-- 0217 refuses more than one live tenant and 0272 refuses to proceed while an
-- archived one still holds records, so there was exactly one workspace these
-- rows could have belonged to when the forward half ran. Ordering by created_at
-- alone would hand every restored row to whichever workspace was created first,
-- archived or not.
--
-- If no live workspace exists and a table is not empty, SET NOT NULL fails and
-- the rollback stops — the honest outcome, since no value this migration could
-- write would be true. A rollback on an empty database (the reverse-and-reapply
-- lane) has nothing to attribute and passes.
ALTER TABLE organization ADD COLUMN workspace_id uuid;
ALTER TABLE organization_domain ADD COLUMN workspace_id uuid;
ALTER TABLE organization_domain_disposition ADD COLUMN workspace_id uuid;
ALTER TABLE organization_fact ADD COLUMN workspace_id uuid;
ALTER TABLE organization_profile_field ADD COLUMN workspace_id uuid;
ALTER TABLE organization_relationship_type ADD COLUMN workspace_id uuid;

DO $$
DECLARE
  live uuid := (SELECT id FROM workspace WHERE archived_at IS NULL ORDER BY created_at LIMIT 1);
  t    text;
BEGIN
  FOREACH t IN ARRAY ARRAY[
    'organization', 'organization_domain', 'organization_domain_disposition',
    'organization_fact', 'organization_profile_field', 'organization_relationship_type'
  ] LOOP
    EXECUTE format('UPDATE %I SET workspace_id = $1 WHERE workspace_id IS NULL', t) USING live;
    EXECUTE format('ALTER TABLE %I ALTER COLUMN workspace_id SET NOT NULL', t);
  END LOOP;
END $$;

ALTER TABLE organization ADD CONSTRAINT organization_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE organization_domain ADD CONSTRAINT organization_domain_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE organization_domain_disposition ADD CONSTRAINT organization_domain_disposition_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE organization_fact ADD CONSTRAINT organization_fact_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE organization_profile_field ADD CONSTRAINT organization_profile_field_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE organization_relationship_type ADD CONSTRAINT organization_relationship_type_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;

-- UNIQUE (id), which is what the migration before this one left: phase B
-- collapsed it from its composite form and this restores that state.
ALTER TABLE organization ADD CONSTRAINT uq_organization_ws_id UNIQUE (id);

DROP INDEX idx_org_class;
DROP INDEX idx_org_lifecycle;
DROP INDEX idx_org_owner;
DROP INDEX idx_org_rel_type_cascade;
DROP INDEX idx_org_rel_type_org;
DROP INDEX idx_domain_disposition_admission;
DROP INDEX idx_domain_disposition_unevidenced;

CREATE INDEX idx_org_class ON organization (workspace_id, classification) WHERE archived_at IS NULL;
CREATE INDEX idx_org_lifecycle ON organization (workspace_id, lifecycle) WHERE archived_at IS NULL;
CREATE INDEX idx_org_owner ON organization (workspace_id, owner_id) WHERE archived_at IS NULL;
CREATE INDEX idx_org_ws_live ON organization (workspace_id) WHERE archived_at IS NULL;
CREATE INDEX idx_org_rel_type_cascade ON organization_relationship_type (workspace_id, organization_id);
CREATE INDEX idx_org_rel_type_org ON organization_relationship_type (workspace_id, organization_id) WHERE archived_at IS NULL;
CREATE INDEX idx_domain_disposition_admission ON organization_domain_disposition (workspace_id, admission_at DESC) WHERE admission IS NOT NULL;
CREATE INDEX idx_domain_disposition_unevidenced ON organization_domain_disposition (workspace_id, updated_at DESC) WHERE pending_reason = 'unevidenced';
