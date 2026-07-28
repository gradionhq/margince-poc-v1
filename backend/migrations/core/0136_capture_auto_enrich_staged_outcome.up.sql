-- ADR-0072/A118 (CAP-PARAM-7): the auto-enrich lane no longer applies a deep
-- read's organization fields and facts directly. A company's website is
-- outsider-controlled text that reaches a model, so the lane now stages the
-- same confirm-first "deepread" proposal a human-requested read stages, and its
-- terminal cursor outcome is 'staged'.
--
-- 'applied' stays in the set because rows written before this migration carry
-- it; nothing writes it any more.
ALTER TABLE capture_auto_enrich_state
  DROP CONSTRAINT capture_auto_enrich_state_last_outcome_check;

ALTER TABLE capture_auto_enrich_state
  ADD CONSTRAINT capture_auto_enrich_state_last_outcome_check
  CHECK (last_outcome IS NULL OR
    last_outcome IN ('queued', 'staged', 'applied', 'empty', 'failed', 'exhausted'));
