-- 0099 down: drop the config-snapshot dimension and revert ai_call to the
-- one-row-per-terminal grain.
ALTER TABLE ai_call DROP CONSTRAINT ai_call_config_fk;
DROP TABLE IF EXISTS ai_call_config;

-- The old grain has no way to express a non-terminal rung or an embedding
-- lane row (it assumes every row is a completion terminal) — delete both
-- before the columns that distinguish them disappear, or they survive as
-- ordinary rows that double-count calls under the reverted schema.
DELETE FROM ai_call WHERE NOT is_terminal OR kind = 'embedding';

ALTER TABLE ai_call DROP CONSTRAINT ai_call_source_check;
ALTER TABLE ai_call DROP CONSTRAINT ai_call_kind_check;
DROP INDEX IF EXISTS ai_call_logical_idx;
ALTER TABLE ai_call
  DROP COLUMN logical_call_id,
  DROP COLUMN attempt,
  DROP COLUMN is_terminal,
  DROP COLUMN attempt_reason,
  DROP COLUMN kind,
  DROP COLUMN served_model,
  DROP COLUMN served_identity_source,
  DROP COLUMN cache_off,
  DROP COLUMN config_hash,
  DROP COLUMN estimated_cost_microusd;
