-- 0062: the keyvault seam's storage (decisions/0023). Two additive changes,
-- no destructive step: the connector_connection.auth bytea column STAYS
-- through this transition (write-to-vault + record ref + backfill) and is
-- dropped only in a LATER additive migration once no reader depends on it.

-- The opaque, workspace-scoped handle the capture module records in place of
-- the raw credential bytes. NULL through the transition: existing rows carry
-- auth until the backfill mints a ref, and a re-authenticated connection then
-- reads credential_ref in preference to auth.
ALTER TABLE connector_connection ADD COLUMN credential_ref text NULL;

-- vault_secret is the local provider's ciphertext store: operational
-- infrastructure, NOT tenant data. It deliberately carries NO workspace_id
-- and therefore no RLS — the workspace lives inside the ref and inside the
-- AES-256-GCM AAD, so isolation is a cryptographic and structural property of
-- the ref (a ref presented under the wrong workspace is rejected before any
-- read, and its ciphertext cannot be opened under another workspace's AAD),
-- not a row-security policy. This mirrors River's operational tables
-- (decisions/0021): scheduling and secret custody are not tenant rows. The
-- ref is the primary key; its random token makes it unguessable. key_version
-- records which root-key version sealed the row so a future rotation can pick
-- the key by version without rewriting the ref format.
CREATE TABLE vault_secret (
  ref         text PRIMARY KEY,
  ciphertext  bytea NOT NULL,
  key_version integer NOT NULL,
  created_at  timestamptz NOT NULL DEFAULT now()
);
