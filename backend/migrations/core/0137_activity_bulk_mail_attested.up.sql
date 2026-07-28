-- ADR-0072/A118 §3: the per-message bulk-mail marker that a REDACTION needs
-- before it may destroy content.
--
-- The noise disposition has two effects with very different costs. Hiding is
-- reversible — the mail is archived, and replying to the sender releases it.
-- Redaction is not: subject and body are nulled in place and the provider
-- original is purged, and nothing brings them back.
--
-- Today both fire on the same evidence: a model said `noise` above the 0.7
-- floor, and seven days passed with nobody objecting. Silence is weak evidence
-- for destroying somebody's correspondence — a rep on holiday, a shared address
-- nobody watches, a workspace that has not looked at its archive.
--
-- It is also the exact shape of an attack. Forge a message as an address the
-- workspace has never written to, write it to read as bulk marketing, and the
-- verdict lands on the ADDRESS: every mail the real owner of that address sends
-- afterwards is hidden within the hour and destroyed a week later, having been
-- seen by nobody, so the documented "reply to recover" escape is unreachable in
-- practice. ADR-0072 already bounds that forward reach to 14 days
-- (noiseVerdictReach) and scopes it to mail with no person link and no attested
-- correspondence. What it does not do is ask for a second opinion before the
-- destruction itself.
--
-- This column is that second opinion: an RFC 2369 List-Unsubscribe header on
-- THIS message, the same signal CAP-PARAM-6's prefix rules already accept as
-- bulk-mail corroboration. Redaction requires it; hiding does not.
--
-- The header is written by the sender, which sounds like the wrong kind of
-- evidence until you check which direction it can be abused in. A sender can
-- add List-Unsubscribe to their own mail and thereby consent to its
-- destruction, which is harmless. What a forger CANNOT do is put the header on
-- their victim's mail — and the victim's real correspondence is precisely what
-- the attack above is aimed at. So the asymmetry runs the safe way: the signal
-- is only ever able to protect the third party it is not attached to.
--
-- Per MESSAGE, never per sender, because the sweep is driven from the mail: a
-- newsletter blast is destroyed and a personal message from the same address is
-- only ever hidden.
--
-- NOT NULL DEFAULT false: every row captured before this migration is
-- un-attested and therefore retained rather than destroyed. Under-attestation
-- keeps hidden mail hidden and undestroyed (recoverable); over-attestation
-- would destroy a real correspondent's mail, so false is the safe default.
ALTER TABLE activity
  ADD COLUMN bulk_mail_attested boolean NOT NULL DEFAULT false;

-- Serves the redaction sweep's scan alone: it looks for hidden, attested-bulk
-- mail, so the index carries neither the un-attested rows (the overwhelming
-- majority — every ordinary message) nor the mail nobody has hidden.
CREATE INDEX idx_activity_bulk_mail_attested
  ON activity (workspace_id, counterparty_email)
  WHERE bulk_mail_attested AND archived_at IS NOT NULL;
