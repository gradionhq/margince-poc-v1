-- 0286: the activity satellites drop the tenant column (ADR-0091 §8 phase D).
--
-- Five tables that hang off an activity rather than being one:
--
--   attachment                   files pinned to a record
--   booking_page                 a host's public scheduling link
--   scheduled_send               a message queued to go out later
--   transcript_read              one pass of a recording through the reader
--   activity_participant_replay  the marker saying a replay has settled a row
--
-- The activity spine itself (activity, activity_link, activity_participant)
-- follows in its own change: activity is named by ~120 sites, and a diff that
-- carried both would be unreviewable.
--
-- Every key here already names something narrower than the tenant: an
-- attachment's provider part, a booking page's slug, a replay's activity.

ALTER TABLE attachment DROP CONSTRAINT attachment_workspace_id_fkey;
ALTER TABLE attachment DROP COLUMN workspace_id;

ALTER TABLE booking_page DROP CONSTRAINT booking_page_workspace_id_fkey;
ALTER TABLE booking_page DROP COLUMN workspace_id;

ALTER TABLE scheduled_send DROP CONSTRAINT scheduled_send_workspace_id_fkey;
ALTER TABLE scheduled_send DROP COLUMN workspace_id;

ALTER TABLE transcript_read DROP CONSTRAINT transcript_read_workspace_id_fkey;
ALTER TABLE transcript_read DROP COLUMN workspace_id;

ALTER TABLE activity_participant_replay DROP CONSTRAINT activity_participant_replay_workspace_id_fkey;
ALTER TABLE activity_participant_replay DROP COLUMN workspace_id;

-- uq_attachment_ws_id is phase B's leftover: a second copy of attachment's own
-- primary key, created as a composite foreign-key target that phase C has since
-- rewritten away. It indexes nothing the primary key does not.
ALTER TABLE attachment DROP CONSTRAINT uq_attachment_ws_id;

-- The six indexes that led with the column, recreated on what actually selects
-- rows: a record's files, a host's page, a due send, a transcript's latest pass.
CREATE INDEX attachment_account_ix ON attachment (organization_id, pinned DESC, created_at DESC) WHERE archived_at IS NULL;
CREATE INDEX idx_attachment_entity ON attachment (entity_type, entity_id) WHERE archived_at IS NULL;
CREATE INDEX idx_booking_page_host ON booking_page (host_user_id) WHERE revoked_at IS NULL;
CREATE INDEX idx_scheduled_send_due ON scheduled_send (scheduled_at) WHERE status = 'scheduled';
CREATE INDEX idx_scheduled_send_owner ON scheduled_send (scheduled_by, status, scheduled_at DESC);
CREATE INDEX idx_transcript_read_latest ON transcript_read (activity_id, created_at DESC);
