-- 0198: record what a settled claim COST, so replaying it costs the same.
--
-- The governed tool surface charges every record an answer hands over against
-- the caller's MCP-SESS-READS bound. A replay hands the same records over
-- again and must charge the same number — and that number cannot be re-derived
-- from the stored body: the envelope's `evidence` list dedupes by record, while
-- the bound counts records SERVED, so an answer that cites one record twice
-- (or names records through a probe that adds no reference) would replay for
-- less than it cost. Cheaper on the retry than on the call is exactly the hole
-- the bound exists to close.
--
-- Zero for a REST claim, and honestly so: that door charges no read bound, so
-- its rows have no cost to record. Nothing reads the column for them.
ALTER TABLE idempotency_key
  ADD COLUMN response_records int NOT NULL DEFAULT 0;
