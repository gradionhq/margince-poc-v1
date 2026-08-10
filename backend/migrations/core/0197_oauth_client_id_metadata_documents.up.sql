-- 0197: Client ID Metadata Documents (ADR-0092 §4, MCP 2026-07-28
-- basic/authorization/client-registration). A client identifies itself with an
-- HTTPS URL that resolves to its own metadata document, instead of registering
-- and being handed an opaque id.
--
-- It is a THIRD provenance on the same table rather than a table of its own,
-- because the row it produces has to be the same row: oauth_grant.client_id
-- carries a foreign key onto oauth_client, and that key is what makes a
-- connection revocable. A CIMD client with no row would be a connection an
-- admin can see the passport for and never the client — or, worse, a grant with
-- nothing to point at.
ALTER TABLE oauth_client DROP CONSTRAINT oauth_client_created_via_check;
ALTER TABLE oauth_client ADD CONSTRAINT oauth_client_created_via_check
  CHECK (created_via IN ('dcr', 'admin', 'cimd'));

-- The cache, and the reason it is a column rather than a process-local map:
-- the metadata document is refetched when it goes stale, and every api replica
-- has to agree on when that is. A per-process cache would let one replica serve
-- a redirect_uris list the client removed an hour ago while another refuses it,
-- which reads to the human as an intermittent consent failure.
--
-- NULL means "never fetched", which is every DCR and admin row: those carry
-- their own metadata and nothing re-reads a document for them. A row whose
-- expiry has passed is refetched before the request that found it may proceed,
-- so a stale document can never authorize a redirect.
ALTER TABLE oauth_client ADD COLUMN metadata_expires_at timestamptz NULL;
