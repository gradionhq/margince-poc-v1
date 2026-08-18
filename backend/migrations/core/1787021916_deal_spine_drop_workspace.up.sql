-- 1787021916: the deal spine drops the tenant column (ADR-0091 §8 phase D).
--
-- The last five of the deals module's twelve, and the ones every other module
-- reaches into: deal, its pipeline and stage catalogue, and the two histories
-- that record how a deal moved.
--
--   deal                   the record itself
--   pipeline, stage        the catalogue it is placed in
--   deal_stage_history     every placement it has had
--   deal_forecast_history  every forecast it has carried
--
-- Their keys already name something narrower than the tenant, and phase B put
-- them there: a pipeline's name is unique installation-wide, its default is a
-- `UNIQUE ((true))` singleton, a stage is unique by (pipeline_id, position).
--
-- Four leftovers go with the column. uq_deal_ws_id, uq_pipeline_ws_id and
-- uq_stage_ws_id are each a second copy of their table's own primary key, and
-- uq_stage_ws_id_pipeline duplicates stage_id_pipeline_unique. All four were
-- composite foreign-key targets that phase C rewrote away; none is referenced
-- by any foreign key today, and none indexes anything its sibling does not.
-- stage_id_pipeline_unique STAYS: deal_stage_in_pipeline still points at it,
-- and it is what stops a deal naming a stage from another pipeline.

ALTER TABLE deal_forecast_history DROP CONSTRAINT deal_forecast_history_workspace_id_fkey;
ALTER TABLE deal_forecast_history DROP COLUMN workspace_id;

ALTER TABLE deal_stage_history DROP CONSTRAINT deal_stage_history_workspace_id_fkey;
ALTER TABLE deal_stage_history DROP COLUMN workspace_id;

ALTER TABLE deal DROP CONSTRAINT uq_deal_ws_id;
ALTER TABLE deal DROP CONSTRAINT deal_workspace_id_fkey;
ALTER TABLE deal DROP COLUMN workspace_id;

ALTER TABLE stage DROP CONSTRAINT uq_stage_ws_id;
ALTER TABLE stage DROP CONSTRAINT uq_stage_ws_id_pipeline;
ALTER TABLE stage DROP CONSTRAINT stage_workspace_id_fkey;
ALTER TABLE stage DROP COLUMN workspace_id;

ALTER TABLE pipeline DROP CONSTRAINT uq_pipeline_ws_id;
ALTER TABLE pipeline DROP CONSTRAINT pipeline_workspace_id_fkey;
ALTER TABLE pipeline DROP COLUMN workspace_id;

-- The indexes that led with the column, recreated on what actually selects
-- rows: a close date, an owner, a partner, a project, a stalled deal's last
-- activity, a history's deal and moment.
--
-- idx_deal_ws_live is NOT recreated. It was (workspace_id) WHERE archived_at IS
-- NULL, and an index on the tenant alone has no narrowed form — without the
-- column the predicate is the whole selection, which is not something an index
-- can be. Same reading idx_quota_ws_live and idx_offer_template_ws got.
CREATE INDEX idx_deal_close ON deal (expected_close_date) WHERE status = 'open' AND archived_at IS NULL;
CREATE INDEX idx_deal_owner ON deal (owner_id) WHERE archived_at IS NULL;
CREATE INDEX idx_deal_partner ON deal (partner_org_id) WHERE partner_org_id IS NOT NULL AND archived_at IS NULL;
CREATE INDEX idx_deal_project ON deal (project_id) WHERE project_id IS NOT NULL AND archived_at IS NULL;
CREATE INDEX idx_deal_stalled ON deal (last_activity_at) WHERE status = 'open' AND archived_at IS NULL;
CREATE INDEX idx_dsh_ws_time ON deal_stage_history (changed_at);
CREATE INDEX idx_deal_forecast_history_deal ON deal_forecast_history (deal_id, changed_at);
