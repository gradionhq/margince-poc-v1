-- The list sorts the people and company screens offer, backed by indexes that
-- match the ORDER BY they actually produce.
--
-- storekit renders `<field> ASC|DESC NULLS LAST, created_at DESC, id DESC`, so
-- a bare single-column index does not serve the sort — the tie-breaker is part
-- of the ordering, and without it Postgres still sorts the whole matched set.
-- Each index below carries the full tuple.
--
-- Every one is partial on `archived_at IS NULL`: the lists exclude archived
-- rows unless the caller opts in (API-LIST-4), so the live rows are what the
-- ordering walks, and a partial index over them is both smaller and the one
-- the planner can use for the default read.
--
-- workspace_id leads every index: it is the tenant predicate on every query,
-- and an index the tenant filter cannot use is an index the planner skips.

CREATE INDEX IF NOT EXISTS idx_person_ws_created_keyset
  ON person (workspace_id, created_at DESC, id DESC)
  WHERE archived_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_person_ws_updated_keyset
  ON person (workspace_id, updated_at DESC, created_at DESC, id DESC)
  WHERE archived_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_person_ws_name_keyset
  ON person (workspace_id, full_name, created_at DESC, id DESC)
  WHERE archived_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_person_ws_owner_keyset
  ON person (workspace_id, owner_id, created_at DESC, id DESC)
  WHERE archived_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_org_ws_created_keyset
  ON organization (workspace_id, created_at DESC, id DESC)
  WHERE archived_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_org_ws_updated_keyset
  ON organization (workspace_id, updated_at DESC, created_at DESC, id DESC)
  WHERE archived_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_org_ws_name_keyset
  ON organization (workspace_id, display_name, created_at DESC, id DESC)
  WHERE archived_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_org_ws_owner_keyset
  ON organization (workspace_id, owner_id, created_at DESC, id DESC)
  WHERE archived_at IS NULL;
