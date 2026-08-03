-- 0173: two ways the signal producers could lose or duplicate work, both
-- found reviewing the 0172 arc.

-- 1. The thread watermark stored only the newest message's timestamp, and a
-- thread was due when that timestamp had MOVED. Two ways that misses work:
-- a message inserted after a scan carrying the same occurred_at does not move
-- the maximum, and a connector backfill filling in older messages moves it
-- backwards. Either way the thread is never read again unless a still-later
-- message happens to arrive.
--
-- The count is the tie-breaker. It changes whenever a message is added at any
-- position, which is exactly the condition the timestamp alone cannot see.
-- Existing rows take 0, so every already-scanned thread is re-read once and
-- then settles — a re-read raises nothing new, because the fingerprint holds.
ALTER TABLE signal_thread_scan ADD COLUMN message_count integer NOT NULL DEFAULT 0;

-- 2. The fingerprint index freed the key on 'resolved'. The reasoning was that
-- a resolved situation recurring is a new fact about the account — true of a
-- situation, false of the EVIDENCE. The fingerprint is kind ∥ organization ∥
-- the message the event was read out of, so freeing it lets the next scan of
-- the same conversation re-raise a signal somebody already settled, citing the
-- same sentence in the same email. A recurrence worth raising cites a
-- different message and so carries a different fingerprint anyway.
DROP INDEX uq_signal_fingerprint;
CREATE UNIQUE INDEX uq_signal_fingerprint ON signal (workspace_id, fingerprint)
  WHERE fingerprint IS NOT NULL AND archived_at IS NULL;
