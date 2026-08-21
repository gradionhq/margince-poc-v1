-- ADR-0091 §8 phase D, the last slice: the append-only ledgers drop the tenant
-- column. No table in this schema carries workspace_id after this.
--
-- These two were held to the end deliberately. The archived-tenant residue gate
-- exempted them BY NAME, because their immutability trigger forbids DELETE: an
-- installation cannot clear residue from them even when it wants to, and
-- demanding it would demand the impossible. That gate said their attribution
-- would go with audit_log's own column at the end of phase D, and this is it.
-- (The gate is in the 0001 baseline now, along with the rest of core's
-- history — it is a schema fact rather than a file to cite.)
--
-- What that costs, stated rather than left to be found: on an installation that
-- ever held more than one workspace, these rows carried the only surviving
-- statement of which organization a historical action belonged to. The down
-- half restores the COLUMN and never the values, and the immutability trigger
-- means no later pass can repair them. A single-organization installation
-- (ADR-0061) loses nothing, because there was one answer for every row.
--
-- All six indexes LEAD with the column, so every one is replaced rather than
-- left to fall with it: DROP COLUMN drops a multi-column index outright, and
-- these six are what the audit reads page by. Each keeps the rest of its own
-- key, so the read it serves is unchanged with the leading column gone.
--
-- SET LOCAL lock_timeout bounds how long this waits to ACQUIRE a lock, not how
-- long it holds one. These are the two largest tables in a mature installation
-- and every mutation writes one of them, so the wait matters — but so does what
-- it does not cover: the six index builds are not CONCURRENTLY (a migration runs
-- in one transaction, which CONCURRENTLY forbids), so each holds a write-blocking
-- SHARE lock for its whole build. On a large ledger that is a maintenance
-- window, not a rolling deploy.
SET LOCAL lock_timeout = '3s';

CREATE INDEX idx_audit_actor_narrow  ON audit_log (actor_id, occurred_at DESC);
CREATE INDEX idx_audit_entity_narrow ON audit_log (entity_type, entity_id, occurred_at DESC);
CREATE INDEX idx_audit_time_narrow   ON audit_log (occurred_at DESC);
DROP INDEX idx_audit_actor;
DROP INDEX idx_audit_entity;
DROP INDEX idx_audit_time;
ALTER INDEX idx_audit_actor_narrow  RENAME TO idx_audit_actor;
ALTER INDEX idx_audit_entity_narrow RENAME TO idx_audit_entity;
ALTER INDEX idx_audit_time_narrow   RENAME TO idx_audit_time;

CREATE INDEX idx_system_log_action_narrow ON system_log (action, occurred_at DESC);
CREATE INDEX idx_system_log_actor_narrow  ON system_log (actor_id, occurred_at DESC);
CREATE INDEX idx_system_log_time_narrow   ON system_log (occurred_at DESC);
DROP INDEX idx_system_log_action;
DROP INDEX idx_system_log_actor;
DROP INDEX idx_system_log_time;
ALTER INDEX idx_system_log_action_narrow RENAME TO idx_system_log_action;
ALTER INDEX idx_system_log_actor_narrow  RENAME TO idx_system_log_actor;
ALTER INDEX idx_system_log_time_narrow   RENAME TO idx_system_log_time;

-- The workspace foreign key on each table falls with the column.
ALTER TABLE audit_log  DROP COLUMN workspace_id;
ALTER TABLE system_log DROP COLUMN workspace_id;
