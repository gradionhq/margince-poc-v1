-- Bind context-bearing AI traces to the exact confirmed company view that
-- shaped the request. Empty scopes + fingerprint is the explicit policy-none
-- state; values remain metadata only, never prompt content.
ALTER TABLE ai_call
  ADD COLUMN context_scopes text[] NOT NULL DEFAULT '{}',
  ADD COLUMN context_fingerprint text NOT NULL DEFAULT '';

ALTER TABLE ai_call ADD CONSTRAINT ai_call_context_scopes_check CHECK (
  context_scopes <@ ARRAY[
    'identity', 'positioning', 'sales', 'offer', 'market', 'proof', 'administrative'
  ]::text[]
);

ALTER TABLE ai_call ADD CONSTRAINT ai_call_context_fingerprint_check CHECK (
  context_fingerprint = '' OR context_fingerprint ~ '^[0-9a-f]{64}$'
);
