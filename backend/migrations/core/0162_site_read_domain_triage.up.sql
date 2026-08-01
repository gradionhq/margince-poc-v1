-- 0162: a third thing a site read can be about — a mail domain nobody has yet
-- decided is a company.
--
-- The triage read starts with no organization, because deciding whether one
-- should exist is the whole point of running it. That is the shape onboarding
-- already has (0101), and for the same reason: the row it will eventually name
-- does not exist when the read begins. A 'company' verdict binds
-- organization_id and stamps confirmed_at as it creates the row, so a bound
-- triage dossier reads exactly like the organization dossier it became.

ALTER TABLE site_read DROP CONSTRAINT site_read_target_kind_check;
ALTER TABLE site_read ADD CONSTRAINT site_read_target_kind_check
  CHECK (target_kind IN ('onboarding', 'organization', 'domain_triage'));

ALTER TABLE site_read DROP CONSTRAINT site_read_target_shape;
ALTER TABLE site_read ADD CONSTRAINT site_read_target_shape CHECK (
  (target_kind = 'onboarding' AND
    (organization_id IS NULL OR (organization_id IS NOT NULL AND confirmed_at IS NOT NULL))) OR
  (target_kind = 'organization' AND organization_id IS NOT NULL) OR
  (target_kind = 'domain_triage' AND
    (organization_id IS NULL OR (organization_id IS NOT NULL AND confirmed_at IS NOT NULL)))
);

-- One in-flight triage per seed url. The seed is derived from the registrable
-- domain, so this is per-domain uniqueness: two senders arriving on a new
-- domain at once buy one crawl, not two.
CREATE UNIQUE INDEX uq_site_read_triage_inflight
  ON site_read (workspace_id, seed_url)
  WHERE target_kind = 'domain_triage' AND status IN ('queued', 'running');
