-- ADR-0082/A127: colleague mail is not CRM correspondence.
--
-- Two additive changes the internal-mail exclusion needs.
--
-- 1. workspace_email_domain gains the provenance CAP-DDL-1 always specified.
--    The set now decides whether a message is STORED at all, not merely whether
--    a colleague gets a contact row, so an operator has to be able to tell a
--    domain the system guessed from a connected mailbox apart from one a human
--    confirmed. Existing rows were all auto-seeded from a mailbox identity, so
--    they backfill to 'mailbox' and unverified — claiming otherwise would
--    assert a human confirmation that never happened.
--
-- 2. activity_participant admits the 'bcc' role. Bcc is only ever visible on
--    the sender's own copy, and there it is a real participant of the message:
--    recording it as 'to' would misstate who was addressed, and dropping it
--    would hide a party from the internal-vs-external decision.

ALTER TABLE workspace_email_domain
  ADD COLUMN IF NOT EXISTS source text NOT NULL DEFAULT 'mailbox',
  ADD COLUMN IF NOT EXISTS verified boolean NOT NULL DEFAULT false;

-- Named so a later migration can alter it; an inline CHECK gets a generated
-- name that differs between installations.
ALTER TABLE workspace_email_domain
  DROP CONSTRAINT IF EXISTS workspace_email_domain_source_check;
ALTER TABLE workspace_email_domain
  ADD CONSTRAINT workspace_email_domain_source_check
  CHECK (source IN ('admin', 'mailbox'));

ALTER TABLE activity_participant
  DROP CONSTRAINT IF EXISTS activity_participant_role_check;
ALTER TABLE activity_participant
  ADD CONSTRAINT activity_participant_role_check
  CHECK (role IN ('from', 'to', 'cc', 'bcc', 'attendee', 'organizer'));
