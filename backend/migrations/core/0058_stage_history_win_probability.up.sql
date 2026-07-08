-- 0058: snapshot the stage's win probability at the moment of change
-- (E03.11 trajectory view). Stage config is mutable; the history row must
-- say what the probability WAS, not what the stage says today — same
-- freeze rationale as amount/currency_at_change beside it. Nullable:
-- rows written before this column have no honest value to backfill.
ALTER TABLE deal_stage_history
  ADD COLUMN win_probability_at_change smallint NULL
    CHECK (win_probability_at_change IS NULL OR win_probability_at_change BETWEEN 0 AND 100);
