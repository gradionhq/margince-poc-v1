-- Deliberately a no-op. This migration declares no grant of its own — it only
-- re-applies the backfills 0035 through 0183 already declare, where row-level
-- security discarded the original write. Reverting it must therefore leave
-- every one of those grants standing; each backfill's own down is what removes
-- its object. A down here that stripped the keys would erase grants a rollback
-- past this version is supposed to keep.
SELECT 1;
