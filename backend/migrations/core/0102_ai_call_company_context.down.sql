ALTER TABLE ai_call
  DROP CONSTRAINT ai_call_context_fingerprint_check,
  DROP CONSTRAINT ai_call_context_scopes_check,
  DROP COLUMN context_fingerprint,
  DROP COLUMN context_scopes;
