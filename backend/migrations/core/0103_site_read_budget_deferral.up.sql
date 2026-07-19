-- 0103: budget exhaustion is durable scheduling state for website reads,
-- never a terminal crawl failure. Deferred reads keep their dossier and own
-- the next eligible attempt boundary.

ALTER TABLE site_read DROP CONSTRAINT site_read_status_check;
ALTER TABLE site_read
  ADD COLUMN status_code text NULL,
  ADD COLUMN status_detail text NULL,
  ADD COLUMN next_attempt_at timestamptz NULL,
  ADD CONSTRAINT site_read_status_check
    CHECK (status IN ('queued','deferred','running','done','partial','failed')),
  ADD CONSTRAINT site_read_deferral_shape CHECK (
    (status = 'deferred' AND status_code = 'budget_deferred' AND
      status_detail IS NOT NULL AND next_attempt_at IS NOT NULL) OR
    (status <> 'deferred' AND status_code IS NULL AND
      status_detail IS NULL AND next_attempt_at IS NULL)
  );

DROP INDEX uq_site_read_org_inflight;
DROP INDEX uq_site_read_onboarding_inflight;
CREATE UNIQUE INDEX uq_site_read_org_inflight
  ON site_read (workspace_id, organization_id, seed_url)
  WHERE target_kind = 'organization' AND status IN ('queued','deferred','running');
CREATE UNIQUE INDEX uq_site_read_onboarding_inflight
  ON site_read (workspace_id, seed_url)
  WHERE target_kind = 'onboarding' AND status IN ('queued','deferred','running');
CREATE INDEX idx_site_read_deferred_due
  ON site_read (workspace_id, next_attempt_at, id)
  WHERE status = 'deferred';
