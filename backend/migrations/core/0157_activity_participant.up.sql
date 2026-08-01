-- 0157: an interaction has participants, and until now the schema could not
-- say so (ADR-0078 / ACT-DDL-3).
--
-- activity_link records which RECORDS an activity concerns. It has no user
-- arm, so nothing anywhere records which of OUR people was actually in the
-- conversation. That single gap is why "who on our team knows this contact"
-- cannot be answered: the derivation that exists today string-matches
-- captured_by = 'human:<uuid>', and connector-captured mail is stamped
-- 'connector:gmail', so the overwhelming majority of rows contribute nothing.
--
-- One column on activity holding the mailbox owner would fix the mail case and
-- nothing else. A meeting has an organizer and N attendees; a thread has a To
-- and three CCs; a group chat has parties that are neither. The calendar
-- connector folds attendees into body text today precisely because there is
-- nowhere structured to put them. So this is a row per participant.
--
-- The three identity arms are not interchangeable. user_id is OUR side, and it
-- is what the interaction edge is keyed on. person_id is a known counterparty.
-- address is the party who never became a record — kept rather than dropped,
-- because an unresolved attendee is a fact about the meeting, and dropping it
-- is what the body-text fold already does badly.

CREATE TABLE activity_participant (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id  uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  activity_id   uuid NOT NULL,
  -- CASCADE, and it costs nothing: each participant is its OWN row, so
  -- removing a deleted user's row leaves every other party to that
  -- conversation untouched — the record that a meeting happened is the
  -- activity, not this row. SET NULL was tried and is wrong twice over: a
  -- bare composite one nulls workspace_id, and a column-scoped one leaves a
  -- user-only row with no identity arm, which the CHECK below refuses. Either
  -- way deleting a user would fail outright.
  user_id       uuid NULL,
  person_id     uuid NULL,
  address       text NULL,
  role          text NOT NULL CHECK (role IN ('from','to','cc','attendee','organizer')),
  created_at    timestamptz NOT NULL DEFAULT now(),
  -- A row that names nobody is not a participant.
  CONSTRAINT activity_participant_identity CHECK (
    user_id IS NOT NULL OR person_id IS NOT NULL OR address IS NOT NULL
  ),
  -- Composite tenant-local FKs, per the repo's FK convention: a bare FK is
  -- checked as the table owner and so bypasses RLS.
  CONSTRAINT activity_participant_activity_fkey
    FOREIGN KEY (workspace_id, activity_id) REFERENCES activity (workspace_id, id) ON DELETE CASCADE,
  CONSTRAINT activity_participant_user_fkey
    FOREIGN KEY (workspace_id, user_id) REFERENCES app_user (workspace_id, id) ON DELETE CASCADE,
  CONSTRAINT activity_participant_person_fkey
    FOREIGN KEY (workspace_id, person_id) REFERENCES person (workspace_id, id) ON DELETE CASCADE
);

-- Capture is idempotent on the source natural key, so re-running it over the
-- same message must not accumulate duplicate participants. coalesce over the
-- three arms because any two of them are NULL on a given row, and a NULL in a
-- unique index does not collide with anything — which would make the whole
-- constraint vacuous for exactly the address-only rows that need it most.
CREATE UNIQUE INDEX uq_activity_participant ON activity_participant (
  activity_id,
  role,
  coalesce(user_id, '00000000-0000-0000-0000-000000000000'::uuid),
  coalesce(person_id, '00000000-0000-0000-0000-000000000000'::uuid),
  coalesce(address, '')
);

-- The two directions the projection recompute walks: "which activities did
-- this user take part in" and "which did this person". Partial, because the
-- arm is NULL on most rows.
CREATE INDEX idx_aparticipant_user   ON activity_participant (workspace_id, user_id, activity_id)
  WHERE user_id IS NOT NULL;
CREATE INDEX idx_aparticipant_person ON activity_participant (workspace_id, person_id, activity_id)
  WHERE person_id IS NOT NULL;
-- Erasure and SAR reach an address-only participant by address alone: that row
-- names a data subject who has no person_id to look up.
CREATE INDEX idx_aparticipant_address ON activity_participant (workspace_id, lower(address))
  WHERE address IS NOT NULL;

ALTER TABLE activity_participant ENABLE ROW LEVEL SECURITY;
ALTER TABLE activity_participant FORCE ROW LEVEL SECURITY;
CREATE POLICY activity_participant_tenant_isolation ON activity_participant
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);

GRANT SELECT, INSERT, UPDATE, DELETE ON activity_participant TO margince_app;
