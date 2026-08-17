-- 0282: capture drops the tenant column (ADR-0091 §8 phase D).
--
-- Twelve tables, and they divide into three honest kinds:
--
--   the connections a human granted  — capture_connection, channel_connection
--   the state those connections keep — capture_sync_state, capture_backfill,
--                                      capture_auto_enrich_state/_budget,
--                                      capture_digest, capture_pending_counterparty
--   what came through them           — raw_capture, capture_trace
--
-- plus the two domain lists the pipeline consults: workspace_email_domain (our
-- own addresses, so an internal message is not mistaken for an inbound one) and
-- capture_freemail_domain (the consumer-mail list that stops gmail.com becoming
-- an organization).
--
-- Every one of them is already keyed on something narrower than the tenant —
-- a connection, a user, a (source_system, source_id) natural key, a domain —
-- so phase B left nothing composite here to collapse. What remains is the
-- column, its foreign key, and six indexes that still lead with it.
--
-- The rename of workspace_email_domain to email_domain is NOT here. It is
-- ADR-0091 §1's final sweep, where the word goes out of the schema everywhere
-- at once; doing it in this slice would leave the tree with one renamed table
-- and eighty that still say workspace.

ALTER TABLE raw_capture DROP CONSTRAINT raw_capture_workspace_id_fkey;
ALTER TABLE raw_capture DROP COLUMN workspace_id;

ALTER TABLE capture_connection DROP CONSTRAINT connector_connection_workspace_id_fkey;
ALTER TABLE capture_connection DROP COLUMN workspace_id;

ALTER TABLE channel_connection DROP CONSTRAINT channel_connection_workspace_id_fkey;
ALTER TABLE channel_connection DROP COLUMN workspace_id;

ALTER TABLE capture_sync_state DROP CONSTRAINT capture_sync_state_workspace_id_fkey;
ALTER TABLE capture_sync_state DROP COLUMN workspace_id;

ALTER TABLE capture_backfill DROP CONSTRAINT capture_backfill_workspace_id_fkey;
ALTER TABLE capture_backfill DROP COLUMN workspace_id;

ALTER TABLE capture_auto_enrich_state DROP CONSTRAINT capture_auto_enrich_state_workspace_id_fkey;
ALTER TABLE capture_auto_enrich_state DROP COLUMN workspace_id;

ALTER TABLE capture_auto_enrich_budget DROP CONSTRAINT capture_auto_enrich_budget_workspace_id_fkey;
ALTER TABLE capture_auto_enrich_budget DROP COLUMN workspace_id;

ALTER TABLE capture_digest DROP CONSTRAINT capture_digest_workspace_id_fkey;
ALTER TABLE capture_digest DROP COLUMN workspace_id;

ALTER TABLE capture_pending_counterparty DROP CONSTRAINT capture_pending_counterparty_workspace_id_fkey;
ALTER TABLE capture_pending_counterparty DROP COLUMN workspace_id;

ALTER TABLE capture_trace DROP CONSTRAINT capture_trace_workspace_id_fkey;
ALTER TABLE capture_trace DROP COLUMN workspace_id;

ALTER TABLE workspace_email_domain DROP CONSTRAINT workspace_email_domain_workspace_id_fkey;
ALTER TABLE workspace_email_domain DROP COLUMN workspace_id;

ALTER TABLE capture_freemail_domain DROP CONSTRAINT capture_freemail_domain_workspace_id_fkey;
ALTER TABLE capture_freemail_domain DROP COLUMN workspace_id;

-- The six indexes that led with the column. DROP COLUMN already removed them
-- outright, so each is recreated on what actually selects rows.
--
-- capture_trace_natural_key is the one to read twice: it is a UNIQUE index and
-- it is what makes the trace idempotent under replay. Its COALESCE on user_id
-- stays, because a connector-level decision has no user and NULLs would
-- otherwise never collide with each other.
CREATE INDEX idx_capture_backfill_conn ON capture_backfill (connection_id, created_at DESC);
CREATE INDEX idx_capture_connection ON capture_connection (provider, status) WHERE archived_at IS NULL;
CREATE INDEX capture_trace_counterparty ON capture_trace (counterparty) WHERE counterparty IS NOT NULL;
CREATE INDEX capture_trace_message ON capture_trace (source_system, source_id);
CREATE UNIQUE INDEX capture_trace_natural_key ON capture_trace
  (COALESCE(user_id, '00000000-0000-0000-0000-000000000000'::uuid), source_system, source_id, stage, outcome);
CREATE INDEX capture_trace_user_window ON capture_trace (user_id, occurred_at DESC);
CREATE INDEX capture_trace_window ON capture_trace (occurred_at DESC);

-- channel_connection carried TWO live-row uniqueness rules, and after the
-- collapse one of them can no longer fire.
--
--   uq_channel_connection_ws  (provider)              WHERE archived_at IS NULL
--   uq_channel_connection_bot (provider, channel_id)  WHERE archived_at IS NULL
--
-- The first is now strictly stronger: it permits ONE live row per provider, so
-- any insert that would collide on (provider, channel_id) collides on
-- (provider) first. The bot rule existed to stop a SECOND WORKSPACE binding a
-- bot another workspace was already polling — two getUpdates consumers racing
-- for the same stream. There is no second workspace to stop.
--
-- It goes rather than staying as a rule that never fires, because the store
-- branches on WHICH constraint refused a connect to tell an admin what to do,
-- and a subsumed index makes that branch depend on which index Postgres happens
-- to check first.
DROP INDEX uq_channel_connection_bot;
