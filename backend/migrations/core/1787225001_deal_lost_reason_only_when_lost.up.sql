-- A lost reason belongs only to a lost deal.
--
-- `deal_lost_reason` is one-directional: it demands a reason when the deal is
-- lost and says nothing about the other direction. A deal closed lost and then
-- re-decided as won therefore kept the reason for the loss, and the won-lost
-- report read a fact about an outcome that had been reversed. Migration 0266
-- named this shape when it chose a different one for the won-without-contract
-- columns.
--
-- The writer clears the column on every non-lost landing
-- (deals/deal_advance.go, stageTransitionPatch). This constraint makes the
-- state unrepresentable rather than merely unwritten.

-- Both statements below block writers on `deal`. Bounding the wait means a
-- long-open transaction fails this migration instead of stalling every write
-- to the table for as long as the migration is willing to queue.
SET LOCAL lock_timeout = '3s';

-- Repair first: the constraint cannot be added while rows violate it, and
-- these rows exist wherever a deal was re-decided before the writer was fixed.
-- The reason described a close that no longer stands, so dropping it loses
-- nothing a report could honestly use.
UPDATE deal
   SET lost_reason = NULL
 WHERE lost_reason IS NOT NULL
   AND status <> 'lost';

ALTER TABLE deal
  ADD CONSTRAINT deal_lost_reason_only_when_lost CHECK (
    lost_reason IS NULL OR status = 'lost');
