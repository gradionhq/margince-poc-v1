-- Irreversible by nature: the rows this cleared named companies that are not
-- partners, and the values are not recorded anywhere this migration could read
-- them back from. The audit log holds the history for anyone who needs it.
--
-- Down is a no-op rather than an error so a rollback of the surrounding release
-- is not blocked by a data repair that cannot be undone.
SELECT 1;
