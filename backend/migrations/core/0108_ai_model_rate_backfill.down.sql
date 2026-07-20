-- Scoped to exactly the rows 0108.up.sql inserted: the fixed backfill
-- effective_date AND the backfill's own (provider, model_id) set — never
-- a bare effective_date match, which could also catch a workspace
-- freshly bootstrapped (via ai.SeedWorkspaceDefaultsTx) on the same
-- calendar day this migration's fixed date names.
DELETE FROM ai_model_rate
WHERE effective_date = DATE '2026-07-20'
  AND (provider, model_id) IN (
    ('anthropic', 'claude-opus-4-8'),
    ('anthropic', 'claude-sonnet-4-6'),
    ('anthropic', 'claude-haiku-4-5-20251001'),
    ('gemini', 'gemini-2.5-pro'),
    ('gemini', 'gemini-2.5-flash'),
    ('openai', 'gpt-5-mini'),
    ('ollama', 'gemma3'),
    ('vllm', 'google/gemma-3-12b-it'),
    ('fake', '')
  );
