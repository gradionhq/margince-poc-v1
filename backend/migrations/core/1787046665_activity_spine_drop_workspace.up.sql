-- 1787034854: the activity spine drops the tenant column (ADR-0091 §8 phase D).
--
-- activity and the two tables that hang off every row of it:
--
--   activity              the record of what happened
--   activity_link         what it is about — a person, a company, a deal
--   activity_participant  who was on it, by seat or by address
--
-- activity is the most-referenced table in the schema after person, which is
-- why it takes a change of its own. Its identity key is already the provider's:
-- uq_activity_source is (source_system, source_id), which is what makes a
-- connector replay free.
--
-- uq_activity_ws_id is phase B's leftover — a second copy of activity's own
-- primary key, created as a composite foreign-key target that phase C rewrote
-- away. It is referenced by no foreign key and indexes nothing activity_pkey
-- does not.

ALTER TABLE activity_participant DROP CONSTRAINT activity_participant_workspace_id_fkey;
ALTER TABLE activity_participant DROP COLUMN workspace_id;

ALTER TABLE activity_link DROP CONSTRAINT activity_link_workspace_id_fkey;
ALTER TABLE activity_link DROP COLUMN workspace_id;

-- The meeting overlap rule is an EXCLUDE constraint, so it names the column in
-- its own definition and cannot be carried through a DROP COLUMN: it is dropped
-- and rebuilt on what it was always really about — one host cannot be in two
-- meetings at once. The tenant was only ever the outer bound around that host.
ALTER TABLE activity DROP CONSTRAINT activity_meeting_no_overlap;
ALTER TABLE activity DROP CONSTRAINT uq_activity_ws_id;
ALTER TABLE activity DROP CONSTRAINT activity_workspace_id_fkey;
ALTER TABLE activity DROP COLUMN workspace_id;

ALTER TABLE activity ADD CONSTRAINT activity_meeting_no_overlap
  EXCLUDE USING gist (
    host_user_id WITH =,
    tsrange(timezone('UTC', occurred_at), timezone('UTC', occurred_at) + interval '1 hour') WITH &&
  ) WHERE (kind = 'meeting' AND host_user_id IS NOT NULL AND archived_at IS NULL);

-- The thirteen indexes that led with the column, recreated on what actually
-- selects rows: a kind, a direction, a thread, a counterparty, a task's
-- assignee, a reminder's moment, a meeting's host, a participant's person.
CREATE INDEX idx_activity_channel_thread ON activity (channel_provider, thread_key) WHERE channel_provider IS NOT NULL;
CREATE INDEX idx_activity_counterparty_email ON activity (counterparty_email) WHERE counterparty_email IS NOT NULL;
CREATE INDEX idx_activity_counterparty_outbound_attested ON activity (counterparty_email) WHERE counterparty_email IS NOT NULL AND counterparty_outbound_attested;
CREATE INDEX idx_activity_direction ON activity (direction, occurred_at DESC) WHERE direction IS NOT NULL AND archived_at IS NULL;
CREATE INDEX idx_activity_kind ON activity (kind, occurred_at DESC) WHERE archived_at IS NULL;
CREATE INDEX idx_activity_labeled ON activity (capture_labeled_at) WHERE capture_labeled_at IS NOT NULL;
CREATE INDEX idx_activity_meeting_host ON activity (host_user_id, occurred_at) WHERE kind = 'meeting' AND archived_at IS NULL;
CREATE INDEX idx_activity_reminders ON activity (remind_at) WHERE kind = 'task' AND remind_at IS NOT NULL AND is_done = false AND archived_at IS NULL;
CREATE INDEX idx_activity_tasks ON activity (assignee_id, due_at) WHERE kind = 'task' AND is_done = false AND archived_at IS NULL;
CREATE INDEX idx_activity_thread ON activity (thread_key) WHERE thread_key IS NOT NULL;
CREATE INDEX idx_activity_unlabeled ON activity (occurred_at) WHERE capture_label IS NULL AND captured_by LIKE 'connector:%' AND kind = 'email';
CREATE INDEX idx_activity_ws_time ON activity (occurred_at DESC) WHERE archived_at IS NULL;
CREATE INDEX idx_aparticipant_address ON activity_participant (lower(address)) WHERE address IS NOT NULL;
CREATE INDEX idx_aparticipant_person ON activity_participant (person_id, activity_id) WHERE person_id IS NOT NULL;
CREATE INDEX idx_aparticipant_user ON activity_participant (user_id, activity_id) WHERE user_id IS NOT NULL;
