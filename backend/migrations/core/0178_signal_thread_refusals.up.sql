-- 0178: bound how many times one conversation may be refused.
--
-- A reading the model cannot be made to produce is TERMINAL for that text: the
-- same messages re-read next pass buy the same refusal. Two earlier answers to
-- that were both wrong in their own direction. Advancing the watermark retired
-- the thread, so whatever it actually says was never raised by anything again.
-- Leaving it due forever kept the signal reachable but let it hold a slot in
-- every pass: dueThreads takes the 200 newest, so enough permanently-refused
-- conversations sit at the head of that list and the backlog behind them is
-- never read — and each one costs its three model calls every hour.
--
-- So the refusal is counted, and the count is pinned to the exact state that
-- was refused. A conversation that has exhausted its attempts drops out of the
-- queue while it stands unchanged; the moment a message is added to it — the
-- same (newest, count) pair the due rule already watches — the pin no longer
-- matches and the conversation is owed fresh attempts, because the text the
-- model choked on is not the text it would be asked about now.
ALTER TABLE signal_thread_scan
  ADD COLUMN refusals integer NOT NULL DEFAULT 0,
  -- The conversation state the refusals were counted against. NULL on every
  -- existing row: nothing has been refused yet, so nothing is pinned.
  ADD COLUMN refused_activity_at timestamptz,
  ADD COLUMN refused_message_count integer;
