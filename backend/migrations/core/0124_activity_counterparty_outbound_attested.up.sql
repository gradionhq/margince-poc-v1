-- ADR-0072/A118 (CAP-PARAM-6, tier T1): the provider-attested outbound marker.
--
-- The T1 correspondence-positive gate spares a counterparty from transactional
-- suppression when the workspace has ever written to that address. The evidence
-- must be trustworthy: activity.direction is derived by comparing the message's
-- From header against the mailbox owner, and From is forgeable, so a spoofed
-- From:owner message delivered to the inbox would otherwise register as the
-- owner's own correspondence and whitelist an arbitrary address past T2.
--
-- This column carries the PROVIDER's attestation instead — Gmail's SENT label,
-- an IMAP \Sent special-use mailbox, Microsoft's SentItems folder — the one
-- signal an attacker who can only write headers cannot set. It is the sole
-- input to the T1 predicate; direction stays a display field.
--
-- NOT NULL DEFAULT false: every existing row, and every record from a provider
-- that attests nothing, is un-attested. Under-attestation suppresses a
-- legitimate counterparty (recoverable, observable in system_log); over-
-- attestation would create records for a forger, so false is the safe default.
ALTER TABLE activity
  ADD COLUMN counterparty_outbound_attested boolean NOT NULL DEFAULT false;

-- Serves the T1 EXISTS lookup alone: it asks only whether an attested outbound
-- activity to one address exists, so the index carries neither the un-attested
-- rows (the overwhelming majority — every inbound mail) nor the rows with no
-- counterparty at all.
CREATE INDEX idx_activity_counterparty_outbound_attested
  ON activity (workspace_id, counterparty_email)
  WHERE counterparty_email IS NOT NULL AND counterparty_outbound_attested;
