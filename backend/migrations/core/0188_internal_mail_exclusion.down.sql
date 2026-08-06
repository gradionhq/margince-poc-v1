-- Reverses 0188. The bcc role is narrowed back out, so any row holding it must
-- go first: leaving them would fail the restored CHECK, and rewriting them to
-- another role would assert a party was addressed differently than they were.

DELETE FROM activity_participant WHERE role = 'bcc';

ALTER TABLE activity_participant
  DROP CONSTRAINT IF EXISTS activity_participant_role_check;
ALTER TABLE activity_participant
  ADD CONSTRAINT activity_participant_role_check
  CHECK (role IN ('from', 'to', 'cc', 'attendee', 'organizer'));

ALTER TABLE workspace_email_domain
  DROP CONSTRAINT IF EXISTS workspace_email_domain_source_check;
ALTER TABLE workspace_email_domain
  DROP COLUMN IF EXISTS verified,
  DROP COLUMN IF EXISTS source;
