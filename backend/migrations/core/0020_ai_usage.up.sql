-- 0020: per-workspace AI metering (ai-operational-spec §6). Every model
-- call upserts one counter row; the §1.3 budget guardrail and the
-- premium-share alarm read the aggregates. These are telemetry
-- counters, not domain records: no audit/outbox ride-along by design —
-- an ai_usage row asserts nothing about a customer, only about spend.

CREATE TABLE ai_usage (
  workspace_id uuid   NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  day          date   NOT NULL,
  task         text   NOT NULL,
  tier         text   NOT NULL,
  calls        bigint NOT NULL DEFAULT 0,
  cached_hits  bigint NOT NULL DEFAULT 0,
  tokens_in    bigint NOT NULL DEFAULT 0,
  tokens_out   bigint NOT NULL DEFAULT 0,
  PRIMARY KEY (workspace_id, day, task, tier)
);

-- Tenant table ⇒ RLS, same deny-on-unset policy as every other
-- (the coverage fitness test refuses a workspace_id table without it).
ALTER TABLE ai_usage ENABLE ROW LEVEL SECURITY;
ALTER TABLE ai_usage FORCE ROW LEVEL SECURITY;
CREATE POLICY ai_usage_tenant_isolation ON ai_usage
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);
