-- One act often proposes several things at once: a site read stages a
-- company-facts proposal plus one lead per person it published, and a
-- captured interaction proposes a person, an organization and a deal
-- together. bundle_id names that act, so the proposals it produced can be
-- read and decided as the one question they were.
--
-- It is a naked grouping id, not a foreign key: there is no bundle entity
-- (ADR-0036 — the staged row IS the authority object, and a second object
-- with its own lifecycle is what produces "approved but never executed").
-- Every member keeps its own diff hash, target version pin, expiry and
-- verdict; the bundle only says they were asked together.
ALTER TABLE approval ADD COLUMN bundle_id uuid NULL;

CREATE INDEX idx_approval_bundle ON approval (workspace_id, bundle_id, created_at)
  WHERE bundle_id IS NOT NULL;
