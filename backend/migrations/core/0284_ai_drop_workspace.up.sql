-- 0284: the AI trace drops the tenant column (ADR-0091 §8 phase D).
--
-- Five tables, all of them a record of work the server did on the
-- installation's behalf, keyed on the work rather than on who asked:
-- ai_call (per-call metadata), ai_call_payload (the opt-in captured
-- prompt/response), ai_usage (metering), ai_model_rate (the price list a cost
-- is computed against), ai_feedback (a human's verdict on a claim).
--
-- ai_call_payload is the one to read twice: it holds prompt and response text,
-- so it is the special-category-adjacent table privacy erases from by
-- identifier and retention ages out at 365d. Neither of those reaches it by
-- tenant — erasure matches the subject's identifiers, retention matches
-- occurred_at — so removing the column changes what neither of them selects.
--
-- The importer (import_run, import_record_map) takes the same step in the same
-- change, in migrations/custom/: those two tables are fork-owned (ADR-0017),
-- and upstream never writes there.

ALTER TABLE ai_call DROP CONSTRAINT ai_call_workspace_id_fkey;
ALTER TABLE ai_call DROP COLUMN workspace_id;

ALTER TABLE ai_call_payload DROP CONSTRAINT ai_call_payload_workspace_id_fkey;
ALTER TABLE ai_call_payload DROP COLUMN workspace_id;

ALTER TABLE ai_usage DROP CONSTRAINT ai_usage_workspace_id_fkey;
ALTER TABLE ai_usage DROP COLUMN workspace_id;

ALTER TABLE ai_model_rate DROP CONSTRAINT ai_model_rate_workspace_id_fkey;
ALTER TABLE ai_model_rate DROP COLUMN workspace_id;

ALTER TABLE ai_feedback DROP CONSTRAINT ai_feedback_workspace_id_fkey;
ALTER TABLE ai_feedback DROP COLUMN workspace_id;

-- The seven indexes that led with the column, recreated on what actually
-- selects rows: a logical call, a correlation, an agent run, a time window, a
-- feedback subject.
--
-- ai_call_ws_time and ai_call_terminal_trace_idx keep their tie-break on id:
-- the trace list pages by (occurred_at DESC, id DESC), and a keyset cursor over
-- a non-unique sort key repeats or skips rows at every page boundary.
CREATE INDEX ai_call_logical_idx ON ai_call (logical_call_id);
CREATE INDEX ai_call_terminal_trace_idx ON ai_call (occurred_at DESC, id DESC) WHERE is_terminal;
CREATE INDEX ai_call_ws_corr ON ai_call (correlation_id);
CREATE INDEX ai_call_ws_run ON ai_call (agent_run_id);
CREATE INDEX ai_call_ws_time ON ai_call (occurred_at DESC);
CREATE INDEX ai_call_payload_ws_time ON ai_call_payload (occurred_at);
CREATE INDEX idx_ai_feedback_subject ON ai_feedback (subject_type, subject_id);
