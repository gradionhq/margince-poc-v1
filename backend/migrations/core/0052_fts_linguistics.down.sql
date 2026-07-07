DROP INDEX IF EXISTS idx_person_name_trgm;
DROP INDEX IF EXISTS idx_org_name_trgm;
DROP INDEX IF EXISTS idx_deal_name_trgm;
DROP INDEX IF EXISTS idx_lead_name_trgm;

ALTER TABLE person DROP COLUMN search_tsv;
ALTER TABLE person ADD COLUMN search_tsv tsvector GENERATED ALWAYS AS (
  to_tsvector('simple', coalesce(full_name,'') || ' ' || coalesce(title,''))
) STORED;
CREATE INDEX idx_person_search ON person USING gin (search_tsv);

ALTER TABLE organization DROP COLUMN search_tsv;
ALTER TABLE organization ADD COLUMN search_tsv tsvector GENERATED ALWAYS AS (
  to_tsvector('simple', coalesce(display_name,'') || ' ' || coalesce(legal_name,'') || ' ' || coalesce(industry,''))
) STORED;
CREATE INDEX idx_org_search ON organization USING gin (search_tsv);

ALTER TABLE deal DROP COLUMN search_tsv;
ALTER TABLE deal ADD COLUMN search_tsv tsvector GENERATED ALWAYS AS (
  to_tsvector('simple', coalesce(name,''))
) STORED;
CREATE INDEX idx_deal_search ON deal USING gin (search_tsv);

ALTER TABLE lead DROP COLUMN search_tsv;
ALTER TABLE lead ADD COLUMN search_tsv tsvector GENERATED ALWAYS AS (
  to_tsvector('simple', coalesce(full_name,'') || ' ' || coalesce(company_name,'') || ' ' || coalesce(title,''))
) STORED;
CREATE INDEX idx_lead_search ON lead USING gin (search_tsv);

ALTER TABLE activity DROP COLUMN search_tsv;
ALTER TABLE activity DROP COLUMN language;
ALTER TABLE activity ADD COLUMN search_tsv tsvector GENERATED ALWAYS AS (
  to_tsvector('simple', coalesce(subject,'') || ' ' || coalesce(body,''))
) STORED;
CREATE INDEX idx_activity_search ON activity USING gin (search_tsv);

DROP FUNCTION IF EXISTS activity_ts_config(text);
DROP FUNCTION IF EXISTS f_unaccent(text);
