-- Deliberately a no-op. This migration declares no grant of its own — it only
-- re-applies the backfills 0035 through 0183 already declare, where row-level
-- security discarded the original write. Reverting to 0189 must therefore leave
-- every one of those grants standing; each backfill's own down is what removes
-- its object. A down here that stripped the keys would erase grants a rollback
-- to 0189 is supposed to keep.
SELECT 1;
