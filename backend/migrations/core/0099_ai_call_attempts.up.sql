-- 0099: ai_call moves from one-row-per-terminal to one-row-per-attempt
-- (spec §4, the certification substrate). A retried, degraded, or escalated
-- completion now leaves every rung it walked as its own row — logical_call_id
-- groups them, attempt orders them, is_terminal names the one the caller
-- actually got. Old rows each become their own one-row logical call: they
-- were already a terminal-only trace, so backfilling logical_call_id = id
-- and leaving attempt/is_terminal at their defaults (1, true) makes them
-- indistinguishable from a fresh single-attempt call under the new grain.
ALTER TABLE ai_call
  ADD COLUMN logical_call_id uuid,
  ADD COLUMN attempt int NOT NULL DEFAULT 1,
  ADD COLUMN is_terminal boolean NOT NULL DEFAULT true,
  ADD COLUMN attempt_reason text NOT NULL DEFAULT '',
  ADD COLUMN kind text NOT NULL DEFAULT 'completion',
  ADD COLUMN served_model text NOT NULL DEFAULT '',
  ADD COLUMN served_identity_source text NOT NULL DEFAULT 'configured',
  ADD COLUMN cache_off boolean NOT NULL DEFAULT false,
  ADD COLUMN config_hash text,
  ADD COLUMN estimated_cost_microusd bigint;
UPDATE ai_call SET logical_call_id = id;
ALTER TABLE ai_call ALTER COLUMN logical_call_id SET NOT NULL;
CREATE INDEX ai_call_logical_idx ON ai_call (workspace_id, logical_call_id);
ALTER TABLE ai_call ADD CONSTRAINT ai_call_kind_check CHECK (kind IN ('completion','embedding'));
ALTER TABLE ai_call ADD CONSTRAINT ai_call_source_check CHECK (served_identity_source IN ('response','echo','configured'));

-- ai_call_config is the run's config-snapshot dimension: hash-keyed,
-- append-only, and deliberately NOT workspace-scoped — a task contract, a
-- routing yaml, and a prompt version are build/deployment facts, not tenant
-- data, so this table carries no workspace_id and no RLS policy (the same
-- posture as the workspace table itself). ai_call rows across every
-- workspace share one row per distinct config combination instead of
-- repeating it per call.
CREATE TABLE ai_call_config (
  hash                 text        PRIMARY KEY,
  task_contract_hash   text        NOT NULL,
  routing_config_hash  text        NOT NULL,
  prompt_version       text        NOT NULL DEFAULT '',
  provider_params      jsonb       NOT NULL DEFAULT '{}'::jsonb,
  created_at           timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE ai_call ADD CONSTRAINT ai_call_config_fk FOREIGN KEY (config_hash) REFERENCES ai_call_config(hash);
