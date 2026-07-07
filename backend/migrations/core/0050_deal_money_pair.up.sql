-- 0050: the money pair as a schema invariant (data-model §6). A deal's
-- amount_minor and currency come together or not at all — a half-money
-- row silently skips the FX freeze at close and only surfaces later as
-- a deal_closed_fx breach far from the cause. The store rejects the
-- pair split at create/update; this CHECK is the net under it.
ALTER TABLE deal
  ADD CONSTRAINT deal_amount_currency_pair
  CHECK ((amount_minor IS NULL) = (currency IS NULL));
