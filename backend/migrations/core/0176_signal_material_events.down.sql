-- Reverse of 0176. The kinds go back to the original six, so any row a
-- producer wrote under a new kind must go first: leaving it would fail the
-- restored CHECK and strand the migration half-applied.

DROP TABLE IF EXISTS signal_thread_scan;

DROP INDEX IF EXISTS uq_signal_fingerprint;
ALTER TABLE signal DROP COLUMN IF EXISTS fingerprint;

DELETE FROM signal WHERE kind IN
  ('contract_ended','new_opportunity','commitment_made','ghosted_thread');

ALTER TABLE signal DROP CONSTRAINT signal_kind_check;
ALTER TABLE signal ADD CONSTRAINT signal_kind_check CHECK (kind IN (
  'stalled_deal','champion_left','reengagement','buying_intent','risk','other'));
