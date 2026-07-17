-- PO-F-1/PO-F-2 fuzzy tier: the candidate set is restricted to rows
-- sharing a name trigram, so a create scores a handful of neighbours
-- instead of walking the workspace. Without these indexes the % operator
-- still answers correctly but seq-scans person/organization on every
-- create, which the create budget cannot carry.
--
-- f_unaccent (0052) is the IMMUTABLE wrapper an expression index needs;
-- the expression mirrors the resolver's own normalization so the index
-- narrows on the same shape the score is computed over.

CREATE INDEX idx_person_name_trgm
  ON person USING gin (f_unaccent(lower(full_name)) gin_trgm_ops)
  WHERE archived_at IS NULL;

CREATE INDEX idx_organization_name_trgm
  ON organization USING gin (f_unaccent(lower(display_name)) gin_trgm_ops)
  WHERE archived_at IS NULL;
