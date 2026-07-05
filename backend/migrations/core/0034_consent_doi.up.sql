-- 0034: double-opt-in confirmation tokens (data-model §3.4 DOI norm).
-- RecordConsentRequest.double_opt_in_token must prove a real round-trip:
-- the server mints the token, stores only its sha256 (like session and
-- passport secrets), and the confirming grant consumes it exactly once.
-- Accepting any caller-supplied value would let the confirmation be
-- fabricated, gutting the Art 7(1) demonstrability claim.

CREATE TABLE consent_doi_token (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  person_id    uuid NOT NULL,
  purpose_id   uuid NOT NULL,
  token_hash   text NOT NULL,               -- sha256 hex; the plaintext never lands
  issued_at    timestamptz NOT NULL DEFAULT now(),
  expires_at   timestamptz NOT NULL,        -- an unclicked mail is a refusal, not a standing credential
  consumed_at  timestamptz NULL,            -- single-use: stamped by the confirming grant
  CONSTRAINT consent_doi_token_hash_unique UNIQUE (workspace_id, token_hash),
  -- Composite tenant-local FKs (0019 posture): the database itself
  -- rejects a token pointing at another workspace's person or purpose.
  CONSTRAINT consent_doi_token_person_id_fkey FOREIGN KEY (workspace_id, person_id)
    REFERENCES person (workspace_id, id) ON DELETE CASCADE,
  CONSTRAINT consent_doi_token_purpose_id_fkey FOREIGN KEY (workspace_id, purpose_id)
    REFERENCES consent_purpose (workspace_id, id) ON DELETE RESTRICT
);
CREATE INDEX idx_consent_doi_token_person
  ON consent_doi_token (workspace_id, person_id, purpose_id);

ALTER TABLE consent_doi_token ENABLE ROW LEVEL SECURITY;
ALTER TABLE consent_doi_token FORCE ROW LEVEL SECURITY;
CREATE POLICY consent_doi_token_tenant_isolation ON consent_doi_token
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);
