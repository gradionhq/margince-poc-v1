-- 0203: the one-line description a company page shows under the title.
--
-- "Supplies architectural glazing, aluminium systems and modular walls to
-- builders, architects and developers" is the sentence a rep writes once so
-- everyone opening the record knows what the company does. Today the page has
-- nowhere to put it: `industry` is a category, the dossier is agent-written
-- prose behind a card, and neither is a short human-owned line.
--
-- A column rather than a governed custom field, for the same reason
-- `linkedin_url` is one (0194): it is part of what a company IS, every
-- installation wants it, and the page renders it unconditionally. A custom
-- field is for what one installation adds.
--
-- No CHECK on the shape beyond a length bound. The value is free prose in the
-- reader's own words, and the only thing worth refusing is a paragraph pasted
-- into a slot the page renders as two lines.
ALTER TABLE organization
  ADD COLUMN description text NULL;

ALTER TABLE organization
  ADD CONSTRAINT organization_description_length
  CHECK (description IS NULL OR length(description) <= 500);
