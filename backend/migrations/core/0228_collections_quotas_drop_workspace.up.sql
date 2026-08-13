-- 0228: collections and quotas drop the tenant column (ADR-0091 §8 phase D).
--
-- Two modules in one migration because neither references the other's tables:
-- phase D depends on phase C per TABLE, and a set with no edges between its
-- members is one reviewable unit rather than two.
--
-- Webhooks is deliberately NOT here. Its tables lose the column together with
-- the retry sweep's per-tenant fan-out — one job row per live workspace, with
-- an integration suite built on a second tenant's parked delivery. Dropping
-- the column first would leave that suite asserting an isolation the schema no
-- longer has, which is worse than leaving both for one slice (§5).
--
-- Same three kinds of object as 0227, and the same rule about names: a
-- narrowed index answers the queries its wide predecessor answered, so it
-- keeps the name. `uq_list_ws_id`, `uq_tag_ws_id` and
-- `webhook_subscription_ws_id_key` are the redundant copies of their tables'
-- primary keys — 0019 created them as composite foreign-key targets, phase C
-- rewrote the last references away, phase B collapsed them to UNIQUE (id).

DROP INDEX idx_list_member_entity;
CREATE INDEX idx_list_member_entity ON list_member (entity_type, entity_id);

DROP INDEX idx_taggable_entity;
CREATE INDEX idx_taggable_entity ON taggable (entity_type, entity_id);

DROP INDEX idx_saved_view_owner;
CREATE INDEX idx_saved_view_owner ON saved_view (owner_id, resource) WHERE archived_at IS NULL;

DROP INDEX idx_quota_owner;
CREATE INDEX idx_quota_owner ON quota (owner_id) WHERE owner_id IS NOT NULL;

DROP INDEX idx_quota_team;
CREATE INDEX idx_quota_team ON quota (team_id) WHERE team_id IS NOT NULL;

-- idx_quota_ws_live indexed the tenant alone over live rows. Without the
-- tenant there is no column left to index: the predicate IS the selection, and
-- a single-column index on nothing is not an index. The queries it served scan
-- or use idx_quota_owner / idx_quota_team.
DROP INDEX idx_quota_ws_live;



ALTER TABLE list DROP CONSTRAINT uq_list_ws_id;
ALTER TABLE tag DROP CONSTRAINT uq_tag_ws_id;

ALTER TABLE list DROP COLUMN workspace_id;
ALTER TABLE list_member DROP COLUMN workspace_id;
ALTER TABLE tag DROP COLUMN workspace_id;
ALTER TABLE taggable DROP COLUMN workspace_id;
ALTER TABLE saved_view DROP COLUMN workspace_id;
ALTER TABLE quota DROP COLUMN workspace_id;
