-- Reverse of 0286: the activity satellites carry the tenant column again.
--
-- The backfill reads the LIVE workspace — archived_at IS NULL, oldest first.
-- 0217 refuses more than one live tenant and 0272 refuses to proceed while an
-- archived one still holds records, so there was exactly one workspace these
-- rows could have belonged to when the forward half ran. Ordering by created_at
-- alone would hand every restored row to whichever workspace was created first,
-- archived or not.
--
-- If no live workspace exists and a table is not empty, SET NOT NULL fails and
-- the rollback stops — the honest outcome, since no value this migration could
-- write would be true. A rollback on an empty database (the reverse-and-reapply
-- lane) has nothing to attribute and passes.
ALTER TABLE attachment ADD COLUMN workspace_id uuid;
ALTER TABLE booking_page ADD COLUMN workspace_id uuid;
ALTER TABLE scheduled_send ADD COLUMN workspace_id uuid;
ALTER TABLE transcript_read ADD COLUMN workspace_id uuid;
ALTER TABLE activity_participant_replay ADD COLUMN workspace_id uuid;

DO $$
DECLARE
  live uuid := (SELECT id FROM workspace WHERE archived_at IS NULL ORDER BY created_at LIMIT 1);
  t    text;
BEGIN
  FOREACH t IN ARRAY ARRAY[
    'attachment', 'booking_page', 'scheduled_send', 'transcript_read',
    'activity_participant_replay'
  ] LOOP
    EXECUTE format('UPDATE %I SET workspace_id = $1 WHERE workspace_id IS NULL', t) USING live;
    EXECUTE format('ALTER TABLE %I ALTER COLUMN workspace_id SET NOT NULL', t);
  END LOOP;
END $$;

ALTER TABLE attachment ADD CONSTRAINT attachment_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE booking_page ADD CONSTRAINT booking_page_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE scheduled_send ADD CONSTRAINT scheduled_send_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE transcript_read ADD CONSTRAINT transcript_read_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE activity_participant_replay ADD CONSTRAINT activity_participant_replay_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;

-- UNIQUE (id), which is what the migration before this one left: phase B
-- collapsed uq_attachment_ws_id from its composite form and this restores that
-- state, not the pre-phase-B one.
ALTER TABLE attachment ADD CONSTRAINT uq_attachment_ws_id UNIQUE (id);

DROP INDEX attachment_account_ix;
DROP INDEX idx_attachment_entity;
DROP INDEX idx_booking_page_host;
DROP INDEX idx_scheduled_send_due;
DROP INDEX idx_scheduled_send_owner;
DROP INDEX idx_transcript_read_latest;

CREATE INDEX attachment_account_ix ON attachment (workspace_id, organization_id, pinned DESC, created_at DESC) WHERE archived_at IS NULL;
CREATE INDEX idx_attachment_entity ON attachment (workspace_id, entity_type, entity_id) WHERE archived_at IS NULL;
CREATE INDEX idx_booking_page_host ON booking_page (workspace_id, host_user_id) WHERE revoked_at IS NULL;
CREATE INDEX idx_scheduled_send_due ON scheduled_send (workspace_id, scheduled_at) WHERE status = 'scheduled';
CREATE INDEX idx_scheduled_send_owner ON scheduled_send (workspace_id, scheduled_by, status, scheduled_at DESC);
CREATE INDEX idx_transcript_read_latest ON transcript_read (workspace_id, activity_id, created_at DESC);
