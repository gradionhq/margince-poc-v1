-- Reverse of 0282: the twelve capture tables get the column, its foreign key
-- and the six wide indexes back.
--
-- The backfill points every row at the LIVE workspace — archived_at IS NULL,
-- oldest first. 0217 already refused to leave more than one live tenant
-- standing and 0272 refuses to proceed while an archived one still holds
-- records, so by the time this migration's forward half ran there was exactly
-- one workspace these rows could have belonged to. This restores the column,
-- not the distinction; the distinction is gone, which is what the gate at 0272
-- is there to make an operator agree to in advance. Ordering by created_at
-- alone would hand every restored row to whichever workspace was created
-- first, archived or not.
--
-- If no live workspace exists and a table is not empty, SET NOT NULL fails and
-- the rollback stops — the honest outcome, since no value this migration could
-- write would be true. A rollback on an empty database (the reverse-and-reapply
-- lane) leaves nothing to attribute and passes.
ALTER TABLE raw_capture ADD COLUMN workspace_id uuid;
ALTER TABLE capture_connection ADD COLUMN workspace_id uuid;
ALTER TABLE channel_connection ADD COLUMN workspace_id uuid;
ALTER TABLE capture_sync_state ADD COLUMN workspace_id uuid;
ALTER TABLE capture_backfill ADD COLUMN workspace_id uuid;
ALTER TABLE capture_auto_enrich_state ADD COLUMN workspace_id uuid;
ALTER TABLE capture_auto_enrich_budget ADD COLUMN workspace_id uuid;
ALTER TABLE capture_digest ADD COLUMN workspace_id uuid;
ALTER TABLE capture_pending_counterparty ADD COLUMN workspace_id uuid;
ALTER TABLE capture_trace ADD COLUMN workspace_id uuid;
ALTER TABLE workspace_email_domain ADD COLUMN workspace_id uuid;
ALTER TABLE capture_freemail_domain ADD COLUMN workspace_id uuid;

DO $$
DECLARE
  live uuid := (SELECT id FROM workspace WHERE archived_at IS NULL ORDER BY created_at LIMIT 1);
  t    text;
BEGIN
  FOREACH t IN ARRAY ARRAY[
    'raw_capture', 'capture_connection', 'channel_connection', 'capture_sync_state',
    'capture_backfill', 'capture_auto_enrich_state', 'capture_auto_enrich_budget',
    'capture_digest', 'capture_pending_counterparty', 'capture_trace',
    'workspace_email_domain', 'capture_freemail_domain'
  ] LOOP
    EXECUTE format('UPDATE %I SET workspace_id = $1 WHERE workspace_id IS NULL', t) USING live;
    EXECUTE format('ALTER TABLE %I ALTER COLUMN workspace_id SET NOT NULL', t);
  END LOOP;
END $$;

-- The foreign keys, each with the ON DELETE the table carried before. capture_trace
-- CASCADEs where the rest RESTRICT: it is a 24-hour diagnostic that a tenant's
-- departure should take with it, not a record whose loss should block one.
ALTER TABLE raw_capture ADD CONSTRAINT raw_capture_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE capture_connection ADD CONSTRAINT connector_connection_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE channel_connection ADD CONSTRAINT channel_connection_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE capture_sync_state ADD CONSTRAINT capture_sync_state_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE capture_backfill ADD CONSTRAINT capture_backfill_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE capture_auto_enrich_state ADD CONSTRAINT capture_auto_enrich_state_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE capture_auto_enrich_budget ADD CONSTRAINT capture_auto_enrich_budget_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE capture_digest ADD CONSTRAINT capture_digest_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE capture_pending_counterparty ADD CONSTRAINT capture_pending_counterparty_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE capture_trace ADD CONSTRAINT capture_trace_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE CASCADE;
ALTER TABLE workspace_email_domain ADD CONSTRAINT workspace_email_domain_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE capture_freemail_domain ADD CONSTRAINT capture_freemail_domain_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;

DROP INDEX idx_capture_backfill_conn;
DROP INDEX idx_capture_connection;
DROP INDEX capture_trace_counterparty;
DROP INDEX capture_trace_message;
DROP INDEX capture_trace_natural_key;
DROP INDEX capture_trace_user_window;
DROP INDEX capture_trace_window;

CREATE INDEX idx_capture_backfill_conn ON capture_backfill (workspace_id, connection_id, created_at DESC);
CREATE INDEX idx_capture_connection ON capture_connection (workspace_id, provider, status) WHERE archived_at IS NULL;
CREATE INDEX capture_trace_counterparty ON capture_trace (workspace_id, counterparty) WHERE counterparty IS NOT NULL;
CREATE INDEX capture_trace_message ON capture_trace (workspace_id, source_system, source_id);
CREATE UNIQUE INDEX capture_trace_natural_key ON capture_trace
  (workspace_id, COALESCE(user_id, '00000000-0000-0000-0000-000000000000'::uuid), source_system, source_id, stage, outcome);
CREATE INDEX capture_trace_user_window ON capture_trace (workspace_id, user_id, occurred_at DESC);
CREATE INDEX capture_trace_window ON capture_trace (workspace_id, occurred_at DESC);

CREATE UNIQUE INDEX uq_channel_connection_bot ON channel_connection (provider, channel_id)
  WHERE archived_at IS NULL;
