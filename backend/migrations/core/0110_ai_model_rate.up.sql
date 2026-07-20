-- 0110: per-(provider, model) price sheet, effective-dated fx_rate-style
-- (ADR-0067 / AIRT-SCHEMA-4). Price-on-read (design decision 4): the
-- meter/router collect usage only and know nothing about money; cost is
-- computed when asked by joining a call's usage to the rate effective on
-- its day (latest effective_date <= call day wins, matching fx_rate's
-- as-of-date resolution). A call with no matching rate row is unpriced
-- (reported as a count, never a silent 0); local providers get explicit
-- all-zero rows so local cost reads as an honest 0, not "no data".
CREATE TABLE ai_model_rate (
  id             uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id   uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  provider       text NOT NULL,           -- ai_call.provider vocabulary
  model_id       text NOT NULL,           -- provider-native id, matches ai_call.model_id
  input_per_mtok_microusd        bigint NOT NULL CHECK (input_per_mtok_microusd >= 0),
  output_per_mtok_microusd       bigint NOT NULL CHECK (output_per_mtok_microusd >= 0),
  cache_read_per_mtok_microusd   bigint NOT NULL DEFAULT 0 CHECK (cache_read_per_mtok_microusd >= 0),
  cache_write_per_mtok_microusd  bigint NOT NULL DEFAULT 0 CHECK (cache_write_per_mtok_microusd >= 0),
  effective_date date NOT NULL,           -- fx_rate semantics: latest row on-or-before the call day wins
  created_at     timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT ai_model_rate_key UNIQUE (workspace_id, provider, model_id, effective_date)
);
CREATE INDEX idx_ai_model_rate_lookup ON ai_model_rate (workspace_id, provider, model_id, effective_date);

-- Tenant table => RLS, same deny-on-unset policy as every other
-- (the coverage fitness test refuses a workspace_id table without it).
ALTER TABLE ai_model_rate ENABLE ROW LEVEL SECURITY;
ALTER TABLE ai_model_rate FORCE ROW LEVEL SECURITY;
CREATE POLICY ai_model_rate_tenant_isolation ON ai_model_rate
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);
