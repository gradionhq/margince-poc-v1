-- ADR-0072/A118 (CAP-PARAM-6, tier T1): the provider-attested outbound marker.
--
-- The T1 correspondence-positive gate spares a counterparty from transactional
-- suppression when the workspace has ever written to that address. The evidence
-- must be trustworthy: activity.direction is derived by comparing the message's
-- From header against the mailbox owner, and From is forgeable, so a spoofed
-- From:owner message delivered to the inbox would otherwise register as the
-- owner's own correspondence and whitelist an arbitrary address past T2.
--
-- This column carries the PROVIDER's filing instead — Gmail's SENT label, an
-- IMAP \Sent special-use mailbox, Microsoft's SentItems folder — the one signal
-- an attacker who can only write headers cannot set. It is the sole input to
-- the T1 predicate, and it is stamped only where the provider's filing and the
-- message's own authorship agree: placement alone is not authorship either,
-- since a server-side rule can file a stranger's mail into the sent container.
--
-- Four residuals this column does NOT close, recorded so nobody later reads
-- them as bugs. (a) An owner-side rule that files spoofed own-domain mail into
-- Sent Items or a \Sent mailbox defeats the conjunction on Graph and IMAP;
-- Gmail is not reachable this way, since SENT is applied by the send path and
-- filters cannot set it. (b) A forged Reply-To that induces one genuine reply
-- attests an address the owner never chose — the reply really was sent, so no
-- signal here can tell the difference. (c) The gate is single-shot by design:
-- one attested message is sufficient evidence, and it is written before the
-- predicate reads it, so an attacker never needs a history. (d) The evidence is
-- workspace-scoped by design — T1 asks whether the WORKSPACE has written to an
-- address, so any member's mailbox answers for all of them; narrowing it to the
-- capturing connection would suppress a colleague's genuine contact, and any
-- member who can capture can already create the record directly.
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
