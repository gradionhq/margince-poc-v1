-- 0073: offer_line_item.price_grounded (E03.21a cont'd) — false only for
-- an AI-drafted line whose price could not be grounded in conversation
-- evidence or the rate card and was left at the honest zero sentinel
-- instead (never a guessed value, P11/no-fabrication); every human-entered
-- or grounded line defaults true. AddStagedOfferLines (modules/deals) is
-- the one writer that ever sets it false, and only paired with
-- unit_price_minor = 0 — the invariant it enforces at insert time.
ALTER TABLE offer_line_item ADD COLUMN price_grounded boolean NOT NULL DEFAULT true;

-- The invariant above ("not grounded ⇒ zero price") is a DB CHECK, not
-- just a Go-level guard at AddStagedOfferLines' one call site (repo rule:
-- fix the invariant, not the call site) — every writer that ever lands a
-- row here (the revision-copy INSERT...SELECT, a future backfill, or a
-- second staging path) is bound by construction. Every existing row
-- defaults price_grounded = true above, so this CHECK is trivially
-- satisfied for all pre-existing data: only a FALSE row is constrained,
-- and no row can be false before this statement runs.
ALTER TABLE offer_line_item
  ADD CONSTRAINT offer_line_item_ungrounded_price_zero
  CHECK (price_grounded OR unit_price_minor = 0);
