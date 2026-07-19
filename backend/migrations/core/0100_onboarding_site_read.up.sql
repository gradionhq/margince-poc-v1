-- 0100: one operational site-read dossier serves both unbound onboarding
-- and enrichment of an existing organization. The dossier owns draft
-- findings and their version/hash; confirmed company truth still lands only
-- in organization/profile/fact tables after a human confirmation.

ALTER TABLE site_read
  ALTER COLUMN organization_id DROP NOT NULL,
  ADD COLUMN target_kind text NOT NULL DEFAULT 'organization'
    CHECK (target_kind IN ('onboarding','organization')),
  ADD COLUMN profile_fields jsonb NOT NULL DEFAULT '[]',
  ADD COLUMN facts jsonb NOT NULL DEFAULT '[]',
  ADD COLUMN people jsonb NOT NULL DEFAULT '[]',
  ADD COLUMN warnings jsonb NOT NULL DEFAULT '[]',
  ADD COLUMN draft_version integer NOT NULL DEFAULT 1 CHECK (draft_version > 0),
  ADD COLUMN proposal_hash text NOT NULL DEFAULT '',
  ADD COLUMN updated_at timestamptz NOT NULL DEFAULT now(),
  ADD COLUMN confirmed_at timestamptz NULL;

ALTER TABLE site_read DROP CONSTRAINT site_read_org_fkey;
ALTER TABLE site_read ADD CONSTRAINT site_read_target_shape CHECK (
  (target_kind = 'onboarding' AND
    (organization_id IS NULL OR (organization_id IS NOT NULL AND confirmed_at IS NOT NULL))) OR
  (target_kind = 'organization' AND organization_id IS NOT NULL)
);
ALTER TABLE site_read ADD CONSTRAINT site_read_org_fkey
  FOREIGN KEY (workspace_id, organization_id)
  REFERENCES organization (workspace_id, id) ON DELETE CASCADE;

DROP INDEX uq_site_read_inflight;
CREATE UNIQUE INDEX uq_site_read_org_inflight
  ON site_read (workspace_id, organization_id, seed_url)
  WHERE target_kind = 'organization' AND status IN ('queued','running');
CREATE UNIQUE INDEX uq_site_read_onboarding_inflight
  ON site_read (workspace_id, seed_url)
  WHERE target_kind = 'onboarding' AND status IN ('queued','running');
