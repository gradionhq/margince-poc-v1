-- Per-activity audience: who may read the CONTENT of an activity once its
-- row is discoverable at all (platform/auth ActivityContentClause).
--
--   workspace     everyone who can discover the row reads it — the default,
--                 and what every row written before this migration keeps
--   participants  the humans on it: the capturing mailbox owner and anyone
--                 stamped as a participant by seat (activity_participant)
--   selected      the participants plus the users and teams named below
--
-- Customer identity is shared across the workspace, so the row scope that
-- used to hide a colleague's correspondence along with the colleague's
-- contacts no longer does; this column is the deliberate, per-message
-- replacement a human sets. It is never set by capture's default path for a
-- linked row, and the restriction guard on `activity` already refuses an
-- UPDATE of a row held under a retention obligation.
ALTER TABLE activity
  ADD COLUMN audience text NOT NULL DEFAULT 'workspace'
  CONSTRAINT activity_audience_check CHECK (audience IN ('workspace', 'participants', 'selected'));

-- The named audience of a `selected` row. Polymorphic by design — a user or a
-- team — and written only by the audience endpoint in the activities module,
-- in the same transaction as the audience column and its audit row.
CREATE TABLE activity_audience_member (
  activity_id  uuid        NOT NULL REFERENCES activity (id) ON DELETE CASCADE,
  subject_type text        NOT NULL CHECK (subject_type IN ('user', 'team')),
  subject_id   uuid        NOT NULL,
  created_by   text        NOT NULL,
  created_at   timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (activity_id, subject_type, subject_id)
);

-- "Which limited activities may I read" walks from the subject.
CREATE INDEX idx_aam_subject ON activity_audience_member (subject_type, subject_id);
