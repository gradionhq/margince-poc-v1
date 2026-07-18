-- 0087: per-call AI metadata (closes foundation AIRT-SCHEMA-N-1). One row
-- per completion terminal — served call, cache hit, or failure — written
-- from the router's single choke point beside the ai_usage meter. Like
-- ai_usage these are telemetry counters, not domain records: no
-- audit/outbox ride-along and no provenance columns — a row asserts spend
-- and routing facts, never anything about a customer. No content lives
-- here (that is ai_call_payload, 0088), so rows are long-lived.
CREATE TABLE ai_call (
  id                  uuid        NOT NULL DEFAULT uuidv7(),
  workspace_id        uuid        NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  correlation_id      uuid,                      -- request/run trace key; NULL if none bound
  task                text        NOT NULL,
  tier                text        NOT NULL DEFAULT '',  -- '' when the call failed before routing
  provider            text        NOT NULL DEFAULT '',
  model_id            text        NOT NULL DEFAULT '',
  request_fingerprint text        NOT NULL,      -- the cache-key digest (reused)
  tokens_in           bigint      NOT NULL DEFAULT 0,
  tokens_out          bigint      NOT NULL DEFAULT 0,  -- reasoning-inclusive
  reasoning_tokens    bigint      NOT NULL DEFAULT 0,   -- breakdown within tokens_out
  cached_tokens       bigint      NOT NULL DEFAULT 0,
  latency_ms          bigint      NOT NULL DEFAULT 0,
  cache_hit           boolean     NOT NULL DEFAULT false,
  degraded            boolean     NOT NULL DEFAULT false,
  error_sentinel      text,                      -- short stable code on the failure path; NULL on success
  agent_run_id        uuid,                      -- set when the call originates inside a runner step
  occurred_at         timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (id),
  -- Composite unique so tenant-local FKs (ai_call_payload) can target
  -- (workspace_id, id) and the database itself rejects a cross-workspace
  -- reference (the schema-fitness composite-tenant-FK invariant).
  CONSTRAINT uq_ai_call_ws_id UNIQUE (workspace_id, id)
);

CREATE INDEX ai_call_ws_time      ON ai_call (workspace_id, occurred_at DESC);
CREATE INDEX ai_call_ws_corr      ON ai_call (workspace_id, correlation_id);
CREATE INDEX ai_call_ws_run       ON ai_call (workspace_id, agent_run_id);

-- Tenant table ⇒ RLS, same deny-on-unset policy as every other
-- (the coverage fitness test refuses a workspace_id table without it).
ALTER TABLE ai_call ENABLE ROW LEVEL SECURITY;
ALTER TABLE ai_call FORCE ROW LEVEL SECURITY;
CREATE POLICY ai_call_tenant_isolation ON ai_call
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);
