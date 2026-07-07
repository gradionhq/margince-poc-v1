-- 0052: full-text search grows linguistics (issue #16). Everything was
-- to_tsvector('simple', …) with no accent folding, no stemming, no
-- weighting, and two different query parsers. This migration:
--   * unaccent everywhere — the German-market daily friction: a search
--     for "Muller" must find "Müller" (Schäfer/Grüner/Zürich…);
--   * proper-noun fields (names) STAY on 'simple' — stemmers mangle
--     names and brands — but gain pg_trgm indexes for the as-you-type
--     quick-find ("Rech" → "Rechnung GmbH", typo-tolerant);
--   * activity free text (email bodies, notes) gains a language column
--     and per-language stemming ("Vertrag" matches "Verträge"), with
--     the unstemmed simple tokens kept alongside so a language-less
--     query still hits; language is captured where the source declares
--     it and NULL means "no stemming" (never a guessed language);
--   * setweight ranks a match in a name/subject above one in a body.
--
-- unaccent() is only STABLE (its dictionary is search_path-dependent),
-- so generated columns and expression indexes reject it; f_unaccent
-- pins the dictionary schema-qualified and is IMMUTABLE — the standard
-- wrapper.

CREATE EXTENSION IF NOT EXISTS unaccent;
CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE OR REPLACE FUNCTION f_unaccent(text)
  RETURNS text
  LANGUAGE sql IMMUTABLE PARALLEL SAFE STRICT
  RETURN public.unaccent('public.unaccent', $1);

-- activity_ts_config maps the row's captured language onto the
-- stemming configuration; anything unknown falls back to 'simple'.
CREATE OR REPLACE FUNCTION activity_ts_config(lang text)
  RETURNS regconfig
  LANGUAGE sql IMMUTABLE PARALLEL SAFE
  RETURN CASE lang
    WHEN 'de' THEN 'german'::regconfig
    WHEN 'en' THEN 'english'::regconfig
    ELSE 'simple'::regconfig
  END;

-- Names: 'simple' + unaccent + weights (A = the record's name, B = the
-- secondary field).
ALTER TABLE person DROP COLUMN search_tsv;
ALTER TABLE person ADD COLUMN search_tsv tsvector GENERATED ALWAYS AS (
  setweight(to_tsvector('simple', f_unaccent(coalesce(full_name,''))), 'A') ||
  setweight(to_tsvector('simple', f_unaccent(coalesce(title,''))), 'B')
) STORED;
CREATE INDEX idx_person_search ON person USING gin (search_tsv);
CREATE INDEX idx_person_name_trgm ON person USING gin (f_unaccent(lower(full_name)) gin_trgm_ops);

ALTER TABLE organization DROP COLUMN search_tsv;
ALTER TABLE organization ADD COLUMN search_tsv tsvector GENERATED ALWAYS AS (
  setweight(to_tsvector('simple', f_unaccent(coalesce(display_name,''))), 'A') ||
  setweight(to_tsvector('simple', f_unaccent(coalesce(legal_name,'') || ' ' || coalesce(industry,''))), 'B')
) STORED;
CREATE INDEX idx_org_search ON organization USING gin (search_tsv);
CREATE INDEX idx_org_name_trgm ON organization USING gin (f_unaccent(lower(display_name)) gin_trgm_ops);

ALTER TABLE deal DROP COLUMN search_tsv;
ALTER TABLE deal ADD COLUMN search_tsv tsvector GENERATED ALWAYS AS (
  setweight(to_tsvector('simple', f_unaccent(coalesce(name,''))), 'A')
) STORED;
CREATE INDEX idx_deal_search ON deal USING gin (search_tsv);
CREATE INDEX idx_deal_name_trgm ON deal USING gin (f_unaccent(lower(name)) gin_trgm_ops);

ALTER TABLE lead DROP COLUMN search_tsv;
ALTER TABLE lead ADD COLUMN search_tsv tsvector GENERATED ALWAYS AS (
  setweight(to_tsvector('simple', f_unaccent(coalesce(full_name,''))), 'A') ||
  setweight(to_tsvector('simple', f_unaccent(coalesce(company_name,''))), 'B') ||
  setweight(to_tsvector('simple', f_unaccent(coalesce(title,''))), 'B')
) STORED;
CREATE INDEX idx_lead_search ON lead USING gin (search_tsv);
CREATE INDEX idx_lead_name_trgm ON lead USING gin (f_unaccent(lower(coalesce(full_name,'') || ' ' || coalesce(company_name,''))) gin_trgm_ops);

-- Activity free text: subject ranks A on simple tokens; subject+body
-- stem under the row's language AND keep their simple tokens (weight
-- B/C), so both a stemmed German query and a plain literal query hit.
ALTER TABLE activity ADD COLUMN language text NULL
  CHECK (language IS NULL OR language IN ('de','en'));
ALTER TABLE activity DROP COLUMN search_tsv;
ALTER TABLE activity ADD COLUMN search_tsv tsvector GENERATED ALWAYS AS (
  setweight(to_tsvector('simple', f_unaccent(coalesce(subject,''))), 'A') ||
  setweight(to_tsvector(activity_ts_config(language), f_unaccent(coalesce(subject,'') || ' ' || coalesce(body,''))), 'B') ||
  setweight(to_tsvector('simple', f_unaccent(coalesce(body,''))), 'C')
) STORED;
CREATE INDEX idx_activity_search ON activity USING gin (search_tsv);
