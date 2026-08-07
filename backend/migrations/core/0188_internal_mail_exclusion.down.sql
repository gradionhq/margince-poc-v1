-- Reverses 0188. The bcc role is narrowed back out, so any row holding it must
-- go first: leaving them would fail the restored CHECK, and rewriting them to
-- another role would assert a party was addressed differently than they were.

-- The migration role is NOSUPERUSER NOBYPASSRLS, so FORCE RLS binds it and an
-- unbound DELETE would match zero rows — leaving the bcc rows in place and
-- failing the restored CHECK below. The workspace loop is what reaches them;
-- lifting the policy to cross workspaces in one statement would leave tenant
-- isolation off on a message-participant table for the length of the migration.
DO $$
DECLARE ws uuid;
BEGIN
  FOR ws IN SELECT id FROM workspace LOOP
    PERFORM set_config('app.workspace_id', ws::text, true);
    DELETE FROM activity_participant
    WHERE (role = 'bcc')
      AND activity_participant.workspace_id = ws;
  END LOOP;
END $$;

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
