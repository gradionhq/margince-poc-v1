-- Phase 5 company-context rollout: durable first-value timing, context-cost
-- trace metadata, and a conservative existing-installation profile backfill.

ALTER TABLE site_read
  ADD COLUMN first_grounded_at timestamptz NULL;

ALTER TABLE ai_call
  ADD COLUMN context_bytes bigint NOT NULL DEFAULT 0 CHECK (context_bytes >= 0),
  ADD COLUMN context_tokens_estimate bigint NOT NULL DEFAULT 0 CHECK (context_tokens_estimate >= 0);

-- Existing canonical anchor columns fill only absent provenance rows. This is
-- intentionally not a crawl and carries no invented evidence.
INSERT INTO organization_profile_field (
  workspace_id, organization_id, field, value, evidence_snippet, source_url,
  confidence, source, captured_by
)
SELECT o.workspace_id, o.id, candidate.field, candidate.value,
       '', '', 1, 'migration', 'system:migration-0105'
FROM organization o
CROSS JOIN LATERAL (VALUES
  ('display_name', o.display_name),
  ('legal_name', o.legal_name),
  ('registered_address', o.address_line1),
  ('industry', o.industry)
) AS candidate(field, value)
WHERE o.is_anchor
  AND o.archived_at IS NULL
  AND NULLIF(btrim(candidate.value), '') IS NOT NULL
ON CONFLICT (workspace_id, organization_id, field) DO NOTHING;
