-- 0046: sticky Commercial Judgement lead-score override (formulas §3.1,
-- A68/ADR-0053, AC-S1). A non-empty `score_override_reason` marks `score`
-- as human-set: the §3 behavioral recompute must NOT overwrite `score`
-- while a reason is in force — it retains the live machine value in
-- `score_computed` instead (so "revert to computed" loses nothing).
-- Clearing the reason (back to NULL) resumes recompute and `score` tracks
-- `score_computed` again. Setting `score` without a reason is rejected
-- (AC-S1) — enforced in the write path; the CHECK below only guards the
-- column's own shape (no empty/whitespace reason ever stored).
--
-- Additive: two nullable columns on the existing `lead` table. RLS is a
-- table property set in 0014 (ENABLE + FORCE) and is untouched by ADD
-- COLUMN, so lead keeps FORCE ROW LEVEL SECURITY.

ALTER TABLE lead
  ADD COLUMN score_override_reason text NULL
    CHECK (score_override_reason IS NULL OR length(btrim(score_override_reason)) > 0),
  ADD COLUMN score_computed integer NULL;
