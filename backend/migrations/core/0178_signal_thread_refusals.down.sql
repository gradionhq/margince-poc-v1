-- Reverse of 0178. Dropping the columns loses the refusal counts, which is
-- correct: without the columns the queue has no way to act on them, and a
-- conversation that was parked for refusals becomes due again — the state the
-- engine held before 0178.
ALTER TABLE signal_thread_scan
  DROP COLUMN IF EXISTS refusals,
  DROP COLUMN IF EXISTS refused_activity_at,
  DROP COLUMN IF EXISTS refused_message_count;
