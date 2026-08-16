-- person_email gains the answer to a question the mail ladder has always asked
-- of this table without being able to: is this address EVIDENCE OF
-- CORRESPONDENCE, or merely an address somebody vouched for?
--
-- The ladder reads "a live person holds this address" as a settled verdict —
-- capture's priorDispositionTx returns 'real' on it, and the noise sweep
-- refuses to touch it. That reading was sound while every row here arrived by
-- mail or by a human's own typing, both of which are correspondence or an
-- assertion by somebody accountable.
--
-- A channel connector can now supply the address its provider's directory holds
-- for the human who sent a direct message. That address identifies the person —
-- which is what it is collected for — but it proves nothing about mail. Left
-- indistinguishable, one direct message from a stranger would mark their address
-- a known counterparty for good: every later bulk mail from it auto-created, and
-- the noise sweep switched off for it permanently.
--
-- The default is TRUE because every existing row is exactly what the ladder has
-- been assuming — mail capture, an import, or a human typing a contact in. Only
-- the channel writer sets it false, and it is the one caller that has no
-- correspondence to point at.
ALTER TABLE person_email
  ADD COLUMN from_correspondence boolean NOT NULL DEFAULT true;

-- The ladder's two readers both filter on it and both key on the address, so
-- they get the column in the index rather than a heap fetch per candidate.
CREATE INDEX idx_person_email_correspondence
  ON person_email (email) WHERE from_correspondence AND archived_at IS NULL;
