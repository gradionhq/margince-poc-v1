-- Reverse of 1787034854: the activity spine carries the tenant column again.
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
ALTER TABLE activity ADD COLUMN workspace_id uuid;
ALTER TABLE activity_link ADD COLUMN workspace_id uuid;
ALTER TABLE activity_participant ADD COLUMN workspace_id uuid;

DO $$
DECLARE
  live uuid := (SELECT id FROM workspace WHERE archived_at IS NULL ORDER BY created_at LIMIT 1);
  t    text;
BEGIN
  FOREACH t IN ARRAY ARRAY['activity', 'activity_link', 'activity_participant'] LOOP
    EXECUTE format('UPDATE %I SET workspace_id = $1 WHERE workspace_id IS NULL', t) USING live;
    EXECUTE format('ALTER TABLE %I ALTER COLUMN workspace_id SET NOT NULL', t);
  END LOOP;
END $$;

ALTER TABLE activity ADD CONSTRAINT activity_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE activity_link ADD CONSTRAINT activity_link_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE activity_participant ADD CONSTRAINT activity_participant_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;

-- UNIQUE (id), which is what the migration before this one left: phase B
-- collapsed it from its composite form and this restores that state.
ALTER TABLE activity ADD CONSTRAINT uq_activity_ws_id UNIQUE (id);

ALTER TABLE activity DROP CONSTRAINT activity_meeting_no_overlap;
ALTER TABLE activity ADD CONSTRAINT activity_meeting_no_overlap
  EXCLUDE USING gist (
    workspace_id WITH =,
    host_user_id WITH =,
    tsrange(timezone('UTC', occurred_at), timezone('UTC', occurred_at) + interval '1 hour') WITH &&
  ) WHERE (kind = 'meeting' AND host_user_id IS NOT NULL AND archived_at IS NULL);

DROP INDEX idx_activity_channel_thread;
DROP INDEX idx_activity_counterparty_email;
DROP INDEX idx_activity_counterparty_outbound_attested;
DROP INDEX idx_activity_direction;
DROP INDEX idx_activity_kind;
DROP INDEX idx_activity_labeled;
DROP INDEX idx_activity_meeting_host;
DROP INDEX idx_activity_reminders;
DROP INDEX idx_activity_tasks;
DROP INDEX idx_activity_thread;
DROP INDEX idx_activity_unlabeled;
DROP INDEX idx_activity_ws_time;
DROP INDEX idx_aparticipant_address;
DROP INDEX idx_aparticipant_person;
DROP INDEX idx_aparticipant_user;

CREATE INDEX idx_activity_channel_thread ON activity (workspace_id, channel_provider, thread_key) WHERE channel_provider IS NOT NULL;
CREATE INDEX idx_activity_counterparty_email ON activity (workspace_id, counterparty_email) WHERE counterparty_email IS NOT NULL;
CREATE INDEX idx_activity_counterparty_outbound_attested ON activity (workspace_id, counterparty_email) WHERE counterparty_email IS NOT NULL AND counterparty_outbound_attested;
CREATE INDEX idx_activity_direction ON activity (workspace_id, direction, occurred_at DESC) WHERE direction IS NOT NULL AND archived_at IS NULL;
CREATE INDEX idx_activity_kind ON activity (workspace_id, kind, occurred_at DESC) WHERE archived_at IS NULL;
CREATE INDEX idx_activity_labeled ON activity (workspace_id, capture_labeled_at) WHERE capture_labeled_at IS NOT NULL;
CREATE INDEX idx_activity_meeting_host ON activity (workspace_id, host_user_id, occurred_at) WHERE kind = 'meeting' AND archived_at IS NULL;
CREATE INDEX idx_activity_reminders ON activity (workspace_id, remind_at) WHERE kind = 'task' AND remind_at IS NOT NULL AND is_done = false AND archived_at IS NULL;
CREATE INDEX idx_activity_tasks ON activity (workspace_id, assignee_id, due_at) WHERE kind = 'task' AND is_done = false AND archived_at IS NULL;
CREATE INDEX idx_activity_thread ON activity (workspace_id, thread_key) WHERE thread_key IS NOT NULL;
CREATE INDEX idx_activity_unlabeled ON activity (workspace_id, occurred_at) WHERE capture_label IS NULL AND captured_by LIKE 'connector:%' AND kind = 'email';
CREATE INDEX idx_activity_ws_time ON activity (workspace_id, occurred_at DESC) WHERE archived_at IS NULL;
CREATE INDEX idx_aparticipant_address ON activity_participant (workspace_id, lower(address)) WHERE address IS NOT NULL;
CREATE INDEX idx_aparticipant_person ON activity_participant (workspace_id, person_id, activity_id) WHERE person_id IS NOT NULL;
CREATE INDEX idx_aparticipant_user ON activity_participant (workspace_id, user_id, activity_id) WHERE user_id IS NOT NULL;
