-- 0059: staged offer lines (E03.21a). An AI-drafted line item enters as
-- 'staged' and is EXCLUDED from server-computed offer totals until a
-- human accepts it — a staged proposal must never move a number the
-- buyer can see. Existing rows were all human-written: default accepted.
ALTER TABLE offer_line_item
  ADD COLUMN proposal_state text NOT NULL DEFAULT 'accepted'
    CHECK (proposal_state IN ('staged','accepted'));
