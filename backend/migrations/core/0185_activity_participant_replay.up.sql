-- 0185: remembering which activities have had their stored headers re-read
-- for further participants (ADR-0078 / ACT-DDL-3).
--
-- The two-end participant backfill (0157 + its job) needs no state at all: its
-- predicate is "an activity with NO participant rows", every selected row
-- gains one, so the remaining set strictly shrinks and the caller runs it
-- until it returns zero. That is what makes it resumable and crash-safe for
-- free.
--
-- Recovering the CCs and meeting attendees cannot borrow that trick. Those
-- activities already HAVE participant rows — the two ends — so no predicate
-- written over activity_participant distinguishes "not yet re-read" from
-- "re-read and the message had no CCs". Most messages have no CCs, so without
-- durable state the run-until-zero loop would re-parse the same thousands of
-- stored originals on every pass and never terminate.
--
-- Hence one row per activity recording that the attempt happened, and what it
-- found. The outcome is kept rather than a bare marker because the cases want
-- different answers later: `none` is settled forever, whereas `unreadable`
-- names a payload this parser could not decompose and `no_owner` an activity
-- whose mailbox could not be identified. Those last two are the sets worth
-- revisiting — one if the parser improves, the other if the connection that
-- produced them is re-authorized.
--
-- It holds no personal data — activity ids and a verdict — so the erasure and
-- SAR engines have nothing to reach here. The CASCADE below is what keeps that
-- true: erase the activity and the marker goes with it.

CREATE TABLE activity_participant_replay (
  workspace_id uuid        NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  activity_id  uuid        NOT NULL,
  replayed_at  timestamptz NOT NULL DEFAULT now(),
  outcome      text        NOT NULL CHECK (outcome IN ('participants', 'none', 'unreadable', 'no_owner')),
  PRIMARY KEY (workspace_id, activity_id),
  -- Composite and tenant-local, per the repo's FK convention: a bare FK is
  -- checked as the table owner and so bypasses RLS.
  CONSTRAINT activity_participant_replay_activity_fkey
    FOREIGN KEY (workspace_id, activity_id) REFERENCES activity (workspace_id, id) ON DELETE CASCADE
);

ALTER TABLE activity_participant_replay ENABLE ROW LEVEL SECURITY;
ALTER TABLE activity_participant_replay FORCE ROW LEVEL SECURITY;
CREATE POLICY activity_participant_replay_tenant_isolation ON activity_participant_replay
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);
