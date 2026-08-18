-- Reverse of 1787021916: the deal spine carries the tenant column again.
--
-- The backfill reads the LIVE workspace — archived_at IS NULL, oldest first.
-- 0217 refuses more than one live tenant and 0272 refuses to proceed while an
-- archived one still holds records, so there was exactly one workspace these
-- rows could have belonged to when the forward half ran. Ordering by created_at
-- alone would hand every restored row to whichever workspace was created first,
-- archived or not.
--
-- If no live workspace exists and a table is not empty, SET NOT NULL fails and
-- the rollback stops — the honest outcome, since no value this migration could
-- write would be true. A rollback on an empty database (the reverse-and-reapply
-- lane) has nothing to attribute and passes.
ALTER TABLE deal ADD COLUMN workspace_id uuid;
ALTER TABLE pipeline ADD COLUMN workspace_id uuid;
ALTER TABLE stage ADD COLUMN workspace_id uuid;
ALTER TABLE deal_stage_history ADD COLUMN workspace_id uuid;
ALTER TABLE deal_forecast_history ADD COLUMN workspace_id uuid;

DO $$
DECLARE
  live uuid := (SELECT id FROM workspace WHERE archived_at IS NULL ORDER BY created_at LIMIT 1);
  t    text;
BEGIN
  FOREACH t IN ARRAY ARRAY[
    'deal', 'pipeline', 'stage', 'deal_stage_history', 'deal_forecast_history'
  ] LOOP
    EXECUTE format('UPDATE %I SET workspace_id = $1 WHERE workspace_id IS NULL', t) USING live;
    EXECUTE format('ALTER TABLE %I ALTER COLUMN workspace_id SET NOT NULL', t);
  END LOOP;
END $$;

ALTER TABLE deal ADD CONSTRAINT deal_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE pipeline ADD CONSTRAINT pipeline_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE stage ADD CONSTRAINT stage_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE deal_stage_history ADD CONSTRAINT deal_stage_history_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE deal_forecast_history ADD CONSTRAINT deal_forecast_history_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;

-- Each restored as UNIQUE (id), which is the shape the migration before this one
-- left: phase B collapsed all four from their composite forms, and this returns
-- that state rather than the pre-phase-B one.
ALTER TABLE deal ADD CONSTRAINT uq_deal_ws_id UNIQUE (id);
ALTER TABLE pipeline ADD CONSTRAINT uq_pipeline_ws_id UNIQUE (id);
ALTER TABLE stage ADD CONSTRAINT uq_stage_ws_id UNIQUE (id);
ALTER TABLE stage ADD CONSTRAINT uq_stage_ws_id_pipeline UNIQUE (id, pipeline_id);

DROP INDEX idx_deal_close;
DROP INDEX idx_deal_owner;
DROP INDEX idx_deal_partner;
DROP INDEX idx_deal_project;
DROP INDEX idx_deal_stalled;
DROP INDEX idx_dsh_ws_time;
DROP INDEX idx_deal_forecast_history_deal;

CREATE INDEX idx_deal_close ON deal (workspace_id, expected_close_date) WHERE status = 'open' AND archived_at IS NULL;
CREATE INDEX idx_deal_owner ON deal (workspace_id, owner_id) WHERE archived_at IS NULL;
CREATE INDEX idx_deal_partner ON deal (workspace_id, partner_org_id) WHERE partner_org_id IS NOT NULL AND archived_at IS NULL;
CREATE INDEX idx_deal_project ON deal (workspace_id, project_id) WHERE project_id IS NOT NULL AND archived_at IS NULL;
CREATE INDEX idx_deal_stalled ON deal (workspace_id, last_activity_at) WHERE status = 'open' AND archived_at IS NULL;
CREATE INDEX idx_deal_ws_live ON deal (workspace_id) WHERE archived_at IS NULL;
CREATE INDEX idx_dsh_ws_time ON deal_stage_history (workspace_id, changed_at);
CREATE INDEX idx_deal_forecast_history_deal ON deal_forecast_history (workspace_id, deal_id, changed_at);
