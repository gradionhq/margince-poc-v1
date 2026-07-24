-- ADR-0072/A118 (PO-F-2a): organization display-name authority.
-- Who last named an organization, so a richer source can overwrite a weaker one
-- without ever clobbering a human. Precedence: human > dossier > signature > domain.
-- DEFAULT 'human' — a human/agent creating an org through the API is authoritative;
-- the ONE automated domain-derived namer (the capture auto-create path) stamps
-- 'domain' explicitly. Backfill marks captured orgs still carrying their raw mail
-- domain as their own name as 'domain', so a later enrichment may replace them.
ALTER TABLE organization
  ADD COLUMN name_source text NOT NULL DEFAULT 'human'
  CHECK (name_source IN ('human', 'dossier', 'signature', 'domain'));

UPDATE organization o
   SET name_source = 'domain'
 WHERE EXISTS (
   SELECT 1 FROM organization_domain d
    WHERE d.organization_id = o.id
      AND d.archived_at IS NULL
      AND lower(d.domain) = lower(o.display_name)
 );
