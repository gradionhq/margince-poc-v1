-- Reverse of 0239: the two webhook tables carry the tenant column again.
--
-- The backfill reads the LIVE workspace, and the predicate is the point: 0217's
-- pre-flight refuses to run against a database holding more than one workspace
-- with archived_at IS NULL, so there is exactly one live row — but an
-- installation that resolved to one organization by ARCHIVING the others still
-- has those rows, and 0217 names that residue explicitly. Ordering by
-- created_at alone would hand every restored row to whichever workspace
-- happened to be created first, archived or not.
--
-- If no live workspace exists and a table is not empty, SET NOT NULL fails and
-- the rollback stops — the honest outcome, since no value this migration could
-- write would be true.

ALTER TABLE webhook_subscription ADD COLUMN workspace_id uuid;
ALTER TABLE webhook_delivery ADD COLUMN workspace_id uuid;

DO $$
DECLARE ws uuid := (SELECT id FROM workspace WHERE archived_at IS NULL ORDER BY created_at LIMIT 1);
BEGIN
  UPDATE webhook_subscription SET workspace_id = ws;
  UPDATE webhook_delivery SET workspace_id = ws;
END $$;

ALTER TABLE webhook_subscription ALTER COLUMN workspace_id SET NOT NULL;
ALTER TABLE webhook_delivery ALTER COLUMN workspace_id SET NOT NULL;

ALTER TABLE webhook_subscription ADD CONSTRAINT webhook_subscription_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE webhook_delivery ADD CONSTRAINT webhook_delivery_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;

ALTER TABLE webhook_subscription ADD CONSTRAINT webhook_subscription_ws_id_key UNIQUE (id);

DROP INDEX idx_webhook_delivery_by_subscription;
CREATE INDEX idx_webhook_delivery_by_subscription ON webhook_delivery (workspace_id, subscription_id, created_at DESC);

DROP INDEX idx_webhook_subscription_live;
CREATE INDEX idx_webhook_subscription_live ON webhook_subscription (workspace_id, state) WHERE archived_at IS NULL;
