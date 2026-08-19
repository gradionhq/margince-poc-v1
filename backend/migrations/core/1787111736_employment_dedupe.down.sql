-- Only the constraint comes off. The rows the up half archived stay archived:
-- reviving them would recreate the duplicates the index was added to end, and
-- a down migration that restores a defect is worse than one that leaves the
-- repair standing. Anything wrongly archived is un-archivable by hand — the
-- rows are all still there.
DROP INDEX IF EXISTS uq_rel_employment;
