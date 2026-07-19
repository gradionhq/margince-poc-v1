DELETE FROM organization_profile_field
WHERE source = 'migration' AND captured_by = 'system:migration-0105';

ALTER TABLE ai_call
  DROP COLUMN context_tokens_estimate,
  DROP COLUMN context_bytes;

ALTER TABLE site_read DROP COLUMN first_grounded_at;
