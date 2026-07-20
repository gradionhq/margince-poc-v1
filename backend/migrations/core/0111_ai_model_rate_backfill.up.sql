-- 0111: ai_model_rate backfill for existing workspaces (ADR-0067). 0110
-- created the table, and ai.SeedWorkspaceDefaultsTx now seeds it for
-- every NEWLY bootstrapped workspace inside the boot transaction — but a
-- workspace that already existed when 0110 shipped got no rate rows at
-- all, so every ai_call it already logged (and every one it logs before
-- an operator adds a rate by hand) prices as UNPRICED forever. This
-- migration lands the exact same starting price sheet for those
-- workspaces, row-for-row identical to
-- backend/internal/modules/ai/pricing.go's SeedModelRates (pinned by
-- TestBackfillMigrationMatchesSeedModelRates in seed_test.go — a hand
-- mirror, not generated, so that test is the drift guard).
--
-- effective_date is the fixed historical date below, NOT now() — a
-- migration must be deterministic and reproducible on replay, and this
-- is the day the seed price sheet itself was authored (SeedModelRates'
-- own doc comment: "verified 2026-07-20").
INSERT INTO ai_model_rate (
  workspace_id, provider, model_id,
  input_per_mtok_microusd, output_per_mtok_microusd,
  cache_read_per_mtok_microusd, cache_write_per_mtok_microusd,
  effective_date
)
SELECT w.id, v.provider, v.model_id,
       v.input_microusd, v.output_microusd,
       v.cache_read_microusd, v.cache_write_microusd,
       DATE '2026-07-20'
FROM workspace w
CROSS JOIN (VALUES
  ('anthropic', 'claude-opus-4-8',           5000000::bigint, 25000000::bigint, 500000::bigint, 6250000::bigint),
  ('anthropic', 'claude-sonnet-4-6',         3000000::bigint, 15000000::bigint, 300000::bigint, 3750000::bigint),
  ('anthropic', 'claude-haiku-4-5-20251001', 1000000::bigint,  5000000::bigint, 100000::bigint, 1250000::bigint),
  ('gemini',    'gemini-2.5-pro',            1250000::bigint, 10000000::bigint, 125000::bigint,       0::bigint),
  ('gemini',    'gemini-2.5-flash',           300000::bigint,  2500000::bigint,  30000::bigint,       0::bigint),
  ('openai',    'gpt-5-mini',                 750000::bigint,  4500000::bigint,  75000::bigint,       0::bigint),
  ('gemini',    'gemini-embedding-001',       150000::bigint,        0::bigint,      0::bigint,       0::bigint),
  ('ollama',    'gemma3',                           0::bigint,        0::bigint,      0::bigint,       0::bigint),
  ('vllm',      'google/gemma-3-12b-it',            0::bigint,        0::bigint,      0::bigint,       0::bigint),
  ('ollama',    'bge-m3',                           0::bigint,        0::bigint,      0::bigint,       0::bigint),
  ('fake',      '',                                 0::bigint,        0::bigint,      0::bigint,       0::bigint)
) AS v(provider, model_id, input_microusd, output_microusd, cache_read_microusd, cache_write_microusd)
ON CONFLICT (workspace_id, provider, model_id, effective_date) DO NOTHING;
