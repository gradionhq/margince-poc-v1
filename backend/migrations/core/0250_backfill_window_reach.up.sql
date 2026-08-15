-- 0250: the connect-time backfill window reaches 24 and 60 months.
--
-- ADR-0106/A157 widens CAP-PARAM-4's closed set from {3, 6, 12} to
-- {3, 6, 12, 24, 60}. The set stays CLOSED and stays a picker — an unbounded
-- window is an unbounded bill, and the picker is where the customer consents
-- to it — and the default stays 6m, so a multi-year import is asked for
-- rather than defaulted into.
--
-- Purely additive: every value that was legal stays legal, so no existing row
-- can fail the new constraint and nothing needs backfilling. The CHECK is
-- restated rather than relaxed to a range because the enum is the point: a
-- window the picker does not offer must not reach this table by any other
-- door.
--
-- Effective set = this migration (the highest-numbered re-statement):
-- 0092's set plus 24 and 60.
ALTER TABLE capture_backfill DROP CONSTRAINT capture_backfill_window_months_check;
ALTER TABLE capture_backfill ADD CONSTRAINT capture_backfill_window_months_check
  CHECK (window_months IN (3, 6, 12, 24, 60));
