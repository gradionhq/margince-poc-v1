-- A mirror row holds a PROJECTION of the incumbent's record, produced by a
-- mapping declaration that can change under it. The fingerprint records which
-- declaration produced this row, so a projection the current mapping would
-- never produce is detectable rather than silent — the flip freezes whatever
-- the mirror holds, and a wrong projection there becomes a durable native row.
--
-- Nullable with no backfill on purpose: a row written before this column is
-- exactly a row whose projection nothing has verified, so NULL reads as stale
-- and the sweep re-projects it.
ALTER TABLE overlay_mirror ADD COLUMN projection_fingerprint text;
