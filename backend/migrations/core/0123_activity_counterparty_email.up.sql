-- ADR-0072/A118 (CAP-DDL-7): the captured-message counterparty identity.
--
-- Capture stamps the normalized counterparty address on the activity row, so
-- the workspace's correspondence — "has this address ever been written to?" —
-- becomes an index-backed EXISTS instead of an unimplementable scan (the row
-- previously carried no participant email). The column is captured from NOW so
-- the correspondence-positive suppression spare (phase 2b, CAP-PARAM-6) has real
-- outbound history the day it ships; 2b derives the outbound signal from an
-- authenticated provider label, not the forgeable From header, before honoring
-- it as a suppression bypass.
--
-- Additive and nullable: a non-mail activity (a call, a system note) carries no
-- counterparty and stores NULL. The partial index serves the EXISTS lookup and
-- skips the NULL rows.
ALTER TABLE activity ADD COLUMN counterparty_email text;

CREATE INDEX idx_activity_counterparty_email
  ON activity (workspace_id, counterparty_email)
  WHERE counterparty_email IS NOT NULL;
