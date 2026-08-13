-- 0227: the signals module drops the tenant column (ADR-0091 §8 phase D).
--
-- Phase D per module, and signals is the first: two tables, six indexes that
-- lead with `workspace_id`, and one unique that exists only to be a composite
-- foreign key's target. What this migration does to them is what every later
-- module's will do to its own, so the shape is worth reading once.
--
-- Three kinds of object go, and only the first is obvious:
--
--   * the COLUMN, and with it the foreign key into `workspace` that PostgreSQL
--     drops alongside its column without being asked;
--   * the INDEXES that lead with it — a tenant-first index on a single-tenant
--     table costs a comparison per row scanned and buys nothing, and leaving
--     them narrows nothing while pretending the phase ran;
--   * `uq_signal_ws_id`, which 0224 collapsed to UNIQUE (id) and which is
--     therefore a second copy of the primary key. It survived phase B because
--     that phase collapsed keys rather than judging them; it exists at all
--     because 0019 needed a (workspace_id, id) target for the composite
--     foreign keys phase C has since rewritten. Nothing references it now
--     (`sigres_signal_fkey` is recorded against `signal_pkey`), so it is
--     redundant storage on every write to the table.
--
-- The index names do NOT change. A narrowed index answers the same queries as
-- the one it replaces, and renaming it would make every later reader diff two
-- names to learn that nothing about its purpose moved.

DROP INDEX idx_signal_open;
CREATE INDEX idx_signal_open ON signal (status, severity, detected_at DESC);

DROP INDEX idx_signal_unresolved;
CREATE INDEX idx_signal_unresolved ON signal (resolution_state, detected_at DESC);

DROP INDEX signal_resolved_org_ix;
CREATE INDEX signal_resolved_org_ix ON signal (resolved_org_id) WHERE resolved_org_id IS NOT NULL;

DROP INDEX signal_entity_ix;
CREATE INDEX signal_entity_ix ON signal (entity_type, entity_id) WHERE entity_id IS NOT NULL;

DROP INDEX idx_signal_owner_private;
CREATE INDEX idx_signal_owner_private ON signal (owner_id)
  WHERE visibility = 'owner' AND archived_at IS NULL;

DROP INDEX idx_sigres_signal;
CREATE INDEX idx_sigres_signal ON signal_resolution (signal_id, created_at DESC);

-- A constraint, not a bare index: 0019 declared it with ALTER TABLE, and
-- Postgres refuses to drop the index out from under the constraint that owns it.
ALTER TABLE signal DROP CONSTRAINT uq_signal_ws_id;

ALTER TABLE signal DROP COLUMN workspace_id;
ALTER TABLE signal_resolution DROP COLUMN workspace_id;
