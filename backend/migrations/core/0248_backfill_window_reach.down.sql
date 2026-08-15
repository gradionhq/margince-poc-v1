-- Back to 0092's set. A down that runs while a 24- or 60-month run exists
-- would fail the restated CHECK, which is the honest outcome: the rows were
-- created under a wider rule and narrowing the rule does not un-create them.
ALTER TABLE capture_backfill DROP CONSTRAINT capture_backfill_window_months_check;
ALTER TABLE capture_backfill ADD CONSTRAINT capture_backfill_window_months_check
  CHECK (window_months IN (3, 6, 12));
