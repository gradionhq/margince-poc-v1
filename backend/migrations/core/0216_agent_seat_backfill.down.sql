-- Deliberately a no-op. The seat this migration writes is an identity other
-- rows come to reference — owner_id on anything the runner creates, and the
-- actor on every audit row it wrote. Deleting it would not revert the
-- migration; it would either fail on a foreign key or orphan the attribution of
-- work that really happened. An installation that wants no agent seat archives
-- it, which the up migration's guard then respects.
SELECT 1;
