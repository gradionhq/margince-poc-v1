-- 0073: offer_line_item.price_grounded (E03.21a cont'd) — false only for
-- an AI-drafted line whose price could not be grounded in conversation
-- evidence or the rate card and was left at the honest zero sentinel
-- instead (never a guessed value, P11/no-fabrication); every human-entered
-- or grounded line defaults true. AddStagedOfferLines (modules/deals) is
-- the one writer that ever sets it false, and only paired with
-- unit_price_minor = 0 — the invariant it enforces at insert time.
ALTER TABLE offer_line_item ADD COLUMN price_grounded boolean NOT NULL DEFAULT true;
