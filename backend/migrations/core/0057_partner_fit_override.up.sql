-- 0057: sticky Commercial Judgement partner-fit override (formulas §17,
-- A68/ADR-0053) — the exact sibling of the lead-score pair in 0046. A
-- non-empty `partner_fit_override_reason` marks `partner_fit_score` as
-- human-set: recompute must NOT overwrite it and retains the live machine
-- value in `partner_fit_score_computed` (so "revert to computed" loses
-- nothing). Clearing the reason resumes recompute. Setting the score
-- without a reason is rejected in the write path; the CHECKs guard only
-- the columns' own shape.
ALTER TABLE partner
  ADD COLUMN partner_fit_score_computed smallint NULL
    CHECK (partner_fit_score_computed IS NULL OR partner_fit_score_computed BETWEEN 0 AND 100),
  ADD COLUMN partner_fit_override_reason text NULL
    CHECK (partner_fit_override_reason IS NULL OR length(btrim(partner_fit_override_reason)) > 0);
