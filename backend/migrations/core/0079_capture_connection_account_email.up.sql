-- 0079: capture_connection.account_email — the provider account (mailbox
-- address) an inbound Gmail Pub/Sub push carries, so a push can resolve
-- emailAddress -> connection (+ workspace) cross-tenant (CAP-DDL-2, ADR-0062).
-- Populated at Connect from the connector's AccountIdentifier seam; NULL for a
-- connection made before this column existed (it stays on the poll until a
-- re-connect). The partial index serves the push lookup.

ALTER TABLE capture_connection ADD COLUMN account_email text NULL;

CREATE INDEX idx_capture_connection_account ON capture_connection (provider, account_email)
  WHERE account_email IS NOT NULL AND archived_at IS NULL;
