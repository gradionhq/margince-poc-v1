-- Two additive hardening fixes.

-- 'connector' is a first-class actor (events.md §2; capture events are
-- connector-actor, §5.7) — the original CHECK mirrored the data-model §11
-- DDL, which omits it (spec defect: fable feedback 13).
ALTER TABLE audit_log DROP CONSTRAINT audit_log_actor_type_check;
ALTER TABLE audit_log ADD CONSTRAINT audit_log_actor_type_check
  CHECK (actor_type IN ('human','agent','connector','system'));

-- created_at is transaction-START time, so ordering the relay poll by it
-- lets a long transaction publish "before" a short one that committed
-- earlier. seq is assigned at INSERT — and because two transactions
-- touching one entity serialize on its row lock, per-entity seq order IS
-- commit order, which is the guarantee events.md §3 needs (no
-- cross-entity order is promised).
--
-- The IDENTITY backfill numbers any PRE-EXISTING unpublished rows in
-- physical order, not commit order: on a deployment that already has
-- traffic, drain the outbox (let the relay publish to zero pending rows)
-- before applying this migration.
ALTER TABLE event_outbox ADD COLUMN seq bigint GENERATED ALWAYS AS IDENTITY;
DROP INDEX idx_event_outbox_unpublished;
CREATE INDEX idx_event_outbox_unpublished ON event_outbox (seq) WHERE published_at IS NULL;
