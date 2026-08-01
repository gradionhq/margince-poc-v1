DROP INDEX IF EXISTS uq_site_read_triage_inflight;

DELETE FROM site_read WHERE target_kind = 'domain_triage';

ALTER TABLE site_read DROP CONSTRAINT site_read_target_shape;
ALTER TABLE site_read ADD CONSTRAINT site_read_target_shape CHECK (
  (target_kind = 'onboarding' AND
    (organization_id IS NULL OR (organization_id IS NOT NULL AND confirmed_at IS NOT NULL))) OR
  (target_kind = 'organization' AND organization_id IS NOT NULL)
);

ALTER TABLE site_read DROP CONSTRAINT site_read_target_kind_check;
ALTER TABLE site_read ADD CONSTRAINT site_read_target_kind_check
  CHECK (target_kind IN ('onboarding', 'organization'));
