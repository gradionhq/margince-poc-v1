-- Deliberately a no-op. This migration writes nothing 0154 does not already
-- declare — it only re-applies it where row-level security discarded the write —
-- so reverting to 0189 must leave the channel_connection grants standing. 0154's
-- own down is what removes them. A down here that stripped the key would erase a
-- grant a rollback to 0189 is supposed to keep.
SELECT 1;
