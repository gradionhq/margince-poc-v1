-- Irreversible by design, and deliberately a no-op rather than a clear.
--
-- Clearing every identity rate would also clear the ones the fixed writer wrote
-- after this migration ran, which are not this migration's to undo. There is no
-- marker distinguishing the two — a rate of 1 on a base-currency invoice is the
-- same fact whichever wrote it — so the down migration removes nothing.
SELECT 1;
