-- 0109: cache-write token capture (ADR-0067 / AIRT-PARAM-47). Anthropic bills
-- cache creation separately from input; the meter must itemize it so the
-- read-side pricer can price it. No cost columns: cost is computed on read.
ALTER TABLE ai_call  ADD COLUMN cache_write_tokens bigint NOT NULL DEFAULT 0;
ALTER TABLE ai_usage ADD COLUMN cache_write_tokens bigint NOT NULL DEFAULT 0;
