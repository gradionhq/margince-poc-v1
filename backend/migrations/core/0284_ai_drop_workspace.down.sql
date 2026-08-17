-- Reverse of 0284: the AI trace carries the tenant column again.
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
ALTER TABLE ai_call ADD COLUMN workspace_id uuid;
ALTER TABLE ai_call_payload ADD COLUMN workspace_id uuid;
ALTER TABLE ai_usage ADD COLUMN workspace_id uuid;
ALTER TABLE ai_model_rate ADD COLUMN workspace_id uuid;
ALTER TABLE ai_feedback ADD COLUMN workspace_id uuid;

DO $$
DECLARE
  live uuid := (SELECT id FROM workspace WHERE archived_at IS NULL ORDER BY created_at LIMIT 1);
  t    text;
BEGIN
  FOREACH t IN ARRAY ARRAY['ai_call', 'ai_call_payload', 'ai_usage', 'ai_model_rate', 'ai_feedback'] LOOP
    EXECUTE format('UPDATE %I SET workspace_id = $1 WHERE workspace_id IS NULL', t) USING live;
    EXECUTE format('ALTER TABLE %I ALTER COLUMN workspace_id SET NOT NULL', t);
  END LOOP;
END $$;

ALTER TABLE ai_call ADD CONSTRAINT ai_call_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE ai_call_payload ADD CONSTRAINT ai_call_payload_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE ai_usage ADD CONSTRAINT ai_usage_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE ai_model_rate ADD CONSTRAINT ai_model_rate_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE ai_feedback ADD CONSTRAINT ai_feedback_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;

DROP INDEX ai_call_logical_idx;
DROP INDEX ai_call_terminal_trace_idx;
DROP INDEX ai_call_ws_corr;
DROP INDEX ai_call_ws_run;
DROP INDEX ai_call_ws_time;
DROP INDEX ai_call_payload_ws_time;
DROP INDEX idx_ai_feedback_subject;

CREATE INDEX ai_call_logical_idx ON ai_call (workspace_id, logical_call_id);
CREATE INDEX ai_call_terminal_trace_idx ON ai_call (workspace_id, occurred_at DESC, id DESC) WHERE is_terminal;
CREATE INDEX ai_call_ws_corr ON ai_call (workspace_id, correlation_id);
CREATE INDEX ai_call_ws_run ON ai_call (workspace_id, agent_run_id);
CREATE INDEX ai_call_ws_time ON ai_call (workspace_id, occurred_at DESC);
CREATE INDEX ai_call_payload_ws_time ON ai_call_payload (workspace_id, occurred_at);
CREATE INDEX idx_ai_feedback_subject ON ai_feedback (workspace_id, subject_type, subject_id);
