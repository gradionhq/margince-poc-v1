-- A legal notice states one block per legal entity: registered name,
-- registered address, register or VAT number. A group publishes several,
-- and the read's multi-entity abstention refuses to GUESS which one the
-- installation belongs to — correctly, because picking wrong writes
-- another company's legal identity. Until now it also discarded the
-- blocks, so the human retyped what the page already stated. Keeping the
-- census turns the abstention into a choice: the read offers the entities
-- it found and the human says which is theirs.
ALTER TABLE site_read
  ADD COLUMN legal_entities jsonb NOT NULL DEFAULT '[]'::jsonb;
