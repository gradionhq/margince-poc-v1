-- The list sorts the people and company screens offer, backed by indexes that
-- match the ORDER BY those lists actually produce.
--
-- Three things decide the shape, and getting any of them wrong yields an index
-- the planner will not use:
--
-- 1. The lists carry NO workspace_id predicate. Row-level security was retired
--    in 0217 and the list query's WHERE is the row-scope clause plus the
--    caller's filters (internal/modules/people/listpage.go). An index leading
--    with workspace_id cannot serve an ordering that never restricts it, so
--    these lead with the sort column itself.
--
-- 2. storekit renders `<field> ASC|DESC NULLS LAST, created_at DESC, id DESC`
--    and the direction is the caller's. A B-tree can be read backwards only
--    when EVERY key reverses, and here the tie-breakers stay DESC in both
--    directions — so the ascending and descending forms are genuinely two
--    indexes, not one read two ways. Only the name sorts get both: they are
--    the A-Z / Z-A the screens put on a header. `created_at`/`updated_at` get
--    the descending form alone, which is the default read and the direction a
--    recency column is asked for.
--
-- 3. Every index is partial on `archived_at IS NULL`. Archived rows are
--    excluded unless the caller opts in (API-LIST-4), so that is the set the
--    default ordering walks, and the partial index is both smaller and usable
--    for it.
--
-- `owner_id` is NOT indexed for sorting here. It orders by a uuid nobody reads
-- as an order, its selectivity as a filter is already served by
-- idx_person_owner / idx_org_owner, and an unused index is a write cost with
-- no read to pay for it.

CREATE INDEX IF NOT EXISTS idx_person_created_keyset
  ON person (created_at DESC, id DESC)
  WHERE archived_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_person_updated_keyset
  ON person (updated_at DESC NULLS LAST, created_at DESC, id DESC)
  WHERE archived_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_person_name_keyset
  ON person (full_name ASC NULLS LAST, created_at DESC, id DESC)
  WHERE archived_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_person_name_keyset_desc
  ON person (full_name DESC NULLS LAST, created_at DESC, id DESC)
  WHERE archived_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_org_created_keyset
  ON organization (created_at DESC, id DESC)
  WHERE archived_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_org_updated_keyset
  ON organization (updated_at DESC NULLS LAST, created_at DESC, id DESC)
  WHERE archived_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_org_name_keyset
  ON organization (display_name ASC NULLS LAST, created_at DESC, id DESC)
  WHERE archived_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_org_name_keyset_desc
  ON organization (display_name DESC NULLS LAST, created_at DESC, id DESC)
  WHERE archived_at IS NULL;
