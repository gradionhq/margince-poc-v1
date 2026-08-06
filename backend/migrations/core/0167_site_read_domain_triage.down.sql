DROP INDEX IF EXISTS uq_site_read_triage_inflight;

-- A BOUND triage read is a finished organization dossier — it names the company
-- its verdict created and carries the evidence that named it. Rolling back the
-- target_kind must not throw that away, so those rows become what they already
-- are: ordinary organization reads. Only the unbound ones, which describe a
-- question no longer askable at this revision, are removed.
-- FINISHED bound reads only. A triage worker caught between binding its
-- organization and finishing still holds a live row, and converting that one
-- could violate uq_site_read_org_inflight when the same organization already
-- has an active read for this seed — a rollback that fails on a race. An
-- unfinished read is discarded below with the rest; it evidenced nothing.
DO $$
DECLARE ws uuid;
BEGIN
  FOR ws IN SELECT id FROM workspace LOOP
    PERFORM set_config('app.workspace_id', ws::text, true);
    UPDATE site_read SET target_kind = 'organization'
     WHERE target_kind = 'domain_triage'
       AND organization_id IS NOT NULL
       AND status NOT IN ('queued', 'deferred', 'running')
       AND NOT EXISTS (
         SELECT 1 FROM site_read live
          WHERE live.workspace_id = site_read.workspace_id
            AND live.organization_id = site_read.organization_id
            AND live.seed_url = site_read.seed_url
            AND live.target_kind = 'organization'
            AND live.status IN ('queued', 'running'));

    DELETE FROM site_read WHERE target_kind = 'domain_triage';
  END LOOP;
END $$;


ALTER TABLE site_read DROP CONSTRAINT site_read_target_shape;
ALTER TABLE site_read ADD CONSTRAINT site_read_target_shape CHECK (
  (target_kind = 'onboarding' AND
    (organization_id IS NULL OR (organization_id IS NOT NULL AND confirmed_at IS NOT NULL))) OR
  (target_kind = 'organization' AND organization_id IS NOT NULL)
);

ALTER TABLE site_read DROP CONSTRAINT site_read_target_kind_check;
ALTER TABLE site_read ADD CONSTRAINT site_read_target_kind_check
  CHECK (target_kind IN ('onboarding', 'organization'));