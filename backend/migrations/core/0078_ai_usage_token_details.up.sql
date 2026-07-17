-- 0078: itemized model usage (spec §3.8). Native providers report reasoning /
-- thinking tokens and prompt-cache reads separately from prompt/completion
-- counts; record them so the §1.3 budget view reflects true spend. Additive,
-- default 0 — old rows and providers that report neither are unaffected.
ALTER TABLE ai_usage
  ADD COLUMN reasoning_tokens bigint NOT NULL DEFAULT 0,
  ADD COLUMN cached_tokens    bigint NOT NULL DEFAULT 0;
