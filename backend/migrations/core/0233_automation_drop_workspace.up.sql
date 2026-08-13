-- 0233: the automation catalog drops the tenant column (ADR-0091 §8 phase D).
--
-- One table, one index, one redundant unique. The catalog was held back from
-- 0232 by migrations 0148/0149 — a one-off reminder repair whose SQL reads
-- automation.workspace_id, replayed by integration suites that have since been
-- retired with their premise.
--
-- idx_automation_ws_key_live is renamed as it narrows, unlike every other index
-- in phase D: `ws` was in the NAME, not just the definition, so keeping it
-- would leave the name claiming a column the index no longer has.

DROP INDEX idx_automation_ws_key_live;
CREATE INDEX idx_automation_key_live ON automation (key) WHERE enabled AND archived_at IS NULL;

ALTER TABLE automation DROP CONSTRAINT uq_automation_ws_id;
ALTER TABLE automation DROP COLUMN workspace_id;
