-- 0266: why a deal was won with no agreement behind it (ADR-0109 §6, A160).
--
-- A won deal now points at a signed contract, or says why it cannot. Both are
-- legitimate — deals genuinely close on a purchase order, a phone call, or an
-- import — and the difference between them is the whole point of recording it:
-- "how many won deals have no paper, and why" becomes a question with an
-- answer, which is what makes the escape hatch honest rather than a loophole.
--
-- TWO COLUMNS, NOT ONE, and deliberately NOT the shape `lost_reason` has. That
-- column is unconstrained free text with a one-directional CHECK, and this
-- tree already carries the defect that produces: a reason from a previous close
-- survives a reopen and describes the wrong outcome. Here the vocabulary is
-- closed, `other` must say what it means, and both columns are cleared on every
-- transition that makes them untrue.
ALTER TABLE deal
  ADD COLUMN won_without_contract_reason text NULL,
  ADD COLUMN won_without_contract_detail text NULL;

ALTER TABLE deal
  ADD CONSTRAINT deal_won_without_contract_reason CHECK (
    won_without_contract_reason IS NULL
    OR won_without_contract_reason IN
       ('imported', 'purchase_order', 'verbal', 'renewal_by_email', 'other'));

-- "Other" with no detail is the reason that explains nothing, which is the
-- state this whole feature exists to refuse. A free-text answer is required
-- exactly where the closed list ran out.
ALTER TABLE deal
  ADD CONSTRAINT deal_won_without_contract_detail CHECK (
    won_without_contract_reason IS DISTINCT FROM 'other'
    OR (won_without_contract_detail IS NOT NULL
        AND btrim(won_without_contract_detail) <> ''));

-- A reason belongs only to a won deal. Without this an open or lost deal could
-- carry an explanation for a win that never happened, and the report that
-- counts them would be counting fiction.
ALTER TABLE deal
  ADD CONSTRAINT deal_won_without_contract_only_when_won CHECK (
    won_without_contract_reason IS NULL OR status = 'won');

COMMENT ON COLUMN deal.won_without_contract_reason IS
  'Why this deal was won with no contract record behind it (ADR-0109 §6). NULL on a deal that has one — the two are distinguishable, which is the point.';

-- The report this exists to make answerable: won deals with no agreement,
-- grouped by reason. Partial because a deal that is not won never has one.
CREATE INDEX deal_won_without_contract_ix
  ON deal (won_without_contract_reason)
  WHERE won_without_contract_reason IS NOT NULL;
