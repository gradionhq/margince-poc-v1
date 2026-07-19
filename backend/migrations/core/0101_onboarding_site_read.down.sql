-- 0101 rollback: restore site_read to its organization-bound shape.
DROP INDEX uq_site_read_onboarding_inflight;
DROP INDEX uq_site_read_org_inflight;

ALTER TABLE site_read DROP CONSTRAINT site_read_org_fkey;
ALTER TABLE site_read DROP CONSTRAINT site_read_target_shape;

DELETE FROM site_read WHERE organization_id IS NULL;

ALTER TABLE site_read
  ALTER COLUMN organization_id SET NOT NULL,
  DROP COLUMN confirmed_at,
  DROP COLUMN updated_at,
  DROP COLUMN proposal_hash,
  DROP COLUMN draft_version,
  DROP COLUMN warnings,
  DROP COLUMN people,
  DROP COLUMN facts,
  DROP COLUMN profile_fields,
  DROP COLUMN target_kind;

ALTER TABLE site_read ADD CONSTRAINT site_read_org_fkey
  FOREIGN KEY (workspace_id, organization_id)
  REFERENCES organization (workspace_id, id) ON DELETE CASCADE;
CREATE UNIQUE INDEX uq_site_read_inflight
  ON site_read (workspace_id, organization_id, seed_url)
  WHERE status IN ('queued','running');
