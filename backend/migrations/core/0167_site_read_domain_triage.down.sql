DROP INDEX IF EXISTS uq_site_read_triage_inflight;

-- A BOUND triage read is a finished organization dossier — it names the company
-- its verdict created and carries the evidence that named it. Rolling back the
-- target_kind must not throw that away, so those rows become what they already
-- are: ordinary organization reads. Only the unbound ones, which describe a
-- question no longer askable at this revision, are removed.
UPDATE site_read SET target_kind = 'organization'
 WHERE target_kind = 'domain_triage' AND organization_id IS NOT NULL;

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
