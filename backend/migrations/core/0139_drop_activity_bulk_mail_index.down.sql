-- Deliberately empty. Down means "undo the drop", and recreating the index is
-- the write-blocking build this migration exists to remove; a down-migration
-- must not reintroduce the hazard. 0137's own down still drops the column.
SELECT 1;
