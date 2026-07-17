-- 0074: apostrophe folding in name search. The 'simple' parser splits
-- "O'Reilly" into the tokens o + reilly, so the natural query "oreilly"
-- (one token) missed the row in global search, and the quick-find LIKE
-- compared against the apostrophe-carrying name and missed it too.
-- f_unaccent already folds the typographic apostrophes (U+2019, U+02BC)
-- to ASCII ', so stripping ' after unaccent covers every spelling.
--
-- Name tsvectors keep their original tokens (a query "reilly" or
-- "o'reilly" still hits) and additionally carry the collapsed parse
-- ("oreilly"); the trigram quick-find expressions collapse on both
-- sides (see storekit.QuickFindClause). Activity folds its subject —
-- names live there — but not its body: prose apostrophes ("don't") are
-- the stemmed parses' job, and the collapsed duplicate would double the
-- body's tsvector for no name-search gain.

CREATE OR REPLACE FUNCTION f_fold_apostrophes(text)
  RETURNS text
  LANGUAGE sql IMMUTABLE PARALLEL SAFE STRICT
  RETURN replace(public.f_unaccent($1), '''', '');

ALTER TABLE person DROP COLUMN search_tsv;
ALTER TABLE person ADD COLUMN search_tsv tsvector GENERATED ALWAYS AS (
  setweight(to_tsvector('simple', f_unaccent(coalesce(full_name,''))), 'A') ||
  setweight(to_tsvector('simple', f_fold_apostrophes(coalesce(full_name,''))), 'A') ||
  setweight(to_tsvector('simple', f_unaccent(coalesce(title,''))), 'B') ||
  setweight(to_tsvector('simple', f_fold_apostrophes(coalesce(title,''))), 'B')
) STORED;
CREATE INDEX idx_person_search ON person USING gin (search_tsv);
DROP INDEX idx_person_name_trgm;
CREATE INDEX idx_person_name_trgm ON person USING gin (f_fold_apostrophes(lower(full_name)) gin_trgm_ops);

ALTER TABLE organization DROP COLUMN search_tsv;
ALTER TABLE organization ADD COLUMN search_tsv tsvector GENERATED ALWAYS AS (
  setweight(to_tsvector('simple', f_unaccent(coalesce(display_name,''))), 'A') ||
  setweight(to_tsvector('simple', f_fold_apostrophes(coalesce(display_name,''))), 'A') ||
  setweight(to_tsvector('simple', f_unaccent(coalesce(legal_name,'') || ' ' || coalesce(industry,''))), 'B') ||
  setweight(to_tsvector('simple', f_fold_apostrophes(coalesce(legal_name,'') || ' ' || coalesce(industry,''))), 'B')
) STORED;
CREATE INDEX idx_org_search ON organization USING gin (search_tsv);
DROP INDEX idx_org_name_trgm;
CREATE INDEX idx_org_name_trgm ON organization USING gin (f_fold_apostrophes(lower(display_name)) gin_trgm_ops);

ALTER TABLE deal DROP COLUMN search_tsv;
ALTER TABLE deal ADD COLUMN search_tsv tsvector GENERATED ALWAYS AS (
  setweight(to_tsvector('simple', f_unaccent(coalesce(name,''))), 'A') ||
  setweight(to_tsvector('simple', f_fold_apostrophes(coalesce(name,''))), 'A')
) STORED;
CREATE INDEX idx_deal_search ON deal USING gin (search_tsv);
DROP INDEX idx_deal_name_trgm;
CREATE INDEX idx_deal_name_trgm ON deal USING gin (f_fold_apostrophes(lower(name)) gin_trgm_ops);

ALTER TABLE lead DROP COLUMN search_tsv;
ALTER TABLE lead ADD COLUMN search_tsv tsvector GENERATED ALWAYS AS (
  setweight(to_tsvector('simple', f_unaccent(coalesce(full_name,''))), 'A') ||
  setweight(to_tsvector('simple', f_fold_apostrophes(coalesce(full_name,''))), 'A') ||
  setweight(to_tsvector('simple', f_unaccent(coalesce(company_name,''))), 'B') ||
  setweight(to_tsvector('simple', f_fold_apostrophes(coalesce(company_name,''))), 'B') ||
  setweight(to_tsvector('simple', f_unaccent(coalesce(title,''))), 'B') ||
  setweight(to_tsvector('simple', f_fold_apostrophes(coalesce(title,''))), 'B')
) STORED;
CREATE INDEX idx_lead_search ON lead USING gin (search_tsv);
DROP INDEX idx_lead_name_trgm;
CREATE INDEX idx_lead_name_trgm ON lead USING gin (f_fold_apostrophes(lower(coalesce(full_name,'') || ' ' || coalesce(company_name,''))) gin_trgm_ops);

ALTER TABLE activity DROP COLUMN search_tsv;
ALTER TABLE activity ADD COLUMN search_tsv tsvector GENERATED ALWAYS AS (
  setweight(to_tsvector('simple', f_unaccent(coalesce(subject,''))), 'A') ||
  setweight(to_tsvector('simple', f_fold_apostrophes(coalesce(subject,''))), 'A') ||
  setweight(to_tsvector(activity_ts_config(language), f_unaccent(coalesce(subject,'') || ' ' || coalesce(body,''))), 'B') ||
  setweight(to_tsvector('simple', f_unaccent(coalesce(body,''))), 'C')
) STORED;
CREATE INDEX idx_activity_search ON activity USING gin (search_tsv);
