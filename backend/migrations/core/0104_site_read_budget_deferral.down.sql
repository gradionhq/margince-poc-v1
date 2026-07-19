-- 0104 rollback: a deferred dossier becomes queued for the pre-deferral worker.
DROP INDEX idx_site_read_deferred_due;
DROP INDEX uq_site_read_onboarding_inflight;
DROP INDEX uq_site_read_org_inflight;

ALTER TABLE site_read DROP CONSTRAINT site_read_deferral_shape;
ALTER TABLE site_read DROP CONSTRAINT site_read_status_check;
UPDATE site_read SET status = 'queued' WHERE status = 'deferred';
ALTER TABLE site_read
  DROP COLUMN next_attempt_at,
  DROP COLUMN status_detail,
  DROP COLUMN status_code,
  ADD CONSTRAINT site_read_status_check
    CHECK (status IN ('queued','running','done','partial','failed'));

CREATE UNIQUE INDEX uq_site_read_org_inflight
  ON site_read (workspace_id, organization_id, seed_url)
  WHERE target_kind = 'organization' AND status IN ('queued','running');
CREATE UNIQUE INDEX uq_site_read_onboarding_inflight
  ON site_read (workspace_id, seed_url)
  WHERE target_kind = 'onboarding' AND status IN ('queued','running');
