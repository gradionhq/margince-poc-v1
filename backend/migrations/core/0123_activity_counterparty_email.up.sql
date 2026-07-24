-- ADR-0072/A118 (CAP-DDL-7): the captured-message counterparty identity.
--
-- Capture stamps the normalized counterparty address on the activity row, so
-- the correspondence-positive predicate — "has the owner ever written to this
-- address?" — is an index-backed EXISTS over outbound activities instead of an
-- unimplementable scan (the row previously carried no participant email). This
-- is what lets the transactional-suppression gate (CAP-PARAM-6) spare a known
-- contact: a human the owner has corresponded with is never suppressed, even
-- when their mail carries a List-Unsubscribe footer.
--
-- Additive and nullable: a non-mail activity (a call, a system note) carries no
-- counterparty and stores NULL. The partial index serves the EXISTS lookup and
-- skips the NULL rows.
ALTER TABLE activity ADD COLUMN counterparty_email text;

CREATE INDEX idx_activity_counterparty_email
  ON activity (workspace_id, counterparty_email)
  WHERE counterparty_email IS NOT NULL;
