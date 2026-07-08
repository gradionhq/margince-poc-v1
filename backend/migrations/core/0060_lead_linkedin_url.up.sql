-- 0060: first-class LinkedIn identity on lead (E12.10/.11, data-model §5).
-- The normalized profile URL is the exact-match dedupe key for
-- LinkedIn-captured leads: capture looks it up before creating. Lookup
-- index, not a UNIQUE — merging duplicates is a human decision (the
-- capture path warns, it does not refuse).
ALTER TABLE lead ADD COLUMN linkedin_url text NULL;
CREATE INDEX idx_lead_linkedin ON lead (workspace_id, linkedin_url)
  WHERE linkedin_url IS NOT NULL;
