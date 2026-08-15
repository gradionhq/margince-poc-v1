-- 0245: one reading of one meeting transcript for the next steps in it.
--
-- S-E04.3. Reading a transcript is a model call that takes seconds and can
-- fail, so it cannot happen inside the request that asks for it. This row is
-- what makes the asking honest: the POST answers 202 with its id, and the
-- client polls it until it is terminal. Deep read (`site_read`, 0085) is the
-- same shape and the same reason.
--
-- The row exists so three answers can be told apart, which is the whole point
-- of showing it to a human:
--
--   running          the model has not answered yet
--   done, 0 proposals  it read the transcript and found nothing to propose
--   failed           it could not read the transcript at all
--
-- Collapsing the last two is the failure mode this feature must not have: a
-- transcript with no commitments in it is a CORRECT empty answer (GATE-AI-1,
-- evidence-or-omit), and a rep who cannot tell that from a broken model will
-- either distrust a good answer or trust a broken one.
--
-- What it deliberately is NOT: an authority object. The proposals this read
-- produced are approval rows, each with its own diff hash, expiry and verdict
-- (ADR-0036 — the staged row IS the authority object). proposal_ids only
-- records which ones this read staged, so the client can say "3 proposals" and
-- send the rep to the inbox. Deciding never consults this table.
CREATE TABLE transcript_read (
  id             uuid PRIMARY KEY,
  workspace_id   uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,

  -- CASCADE: a reading of a deleted transcript has nothing left to be a
  -- reading OF. The proposals it staged are approval rows and outlive it;
  -- they carry their own copy of the lines they cite (approval.evidence),
  -- precisely so a citation stays readable when the read record is gone.
  activity_id    uuid NOT NULL REFERENCES activity(id) ON DELETE CASCADE,

  status         text NOT NULL DEFAULT 'queued',
  -- Why it ended the way it did, in words a rep can act on. Set on failure;
  -- also set on a done-but-empty read to say the transcript stated no next
  -- steps, rather than leaving an empty result to explain itself.
  status_detail  text NULL,

  -- How many lines the reading addressed, so a cited line number can be shown
  -- against the size of what was read ("line 12 of 48"). It is the count at
  -- READ time: a later body edit re-normalizes and can change it, and the
  -- proposal's own evidence is what stays authoritative about what it saw.
  line_count     integer NOT NULL DEFAULT 0,

  -- The approvals this read staged. A naked id array, not a join table, for
  -- the same reason bundle_id is a naked id (0200): there is no membership
  -- entity, only a record of what one act produced.
  proposal_ids   uuid[] NOT NULL DEFAULT '{}',

  requested_by   text NOT NULL,
  started_at     timestamptz NULL,
  finished_at    timestamptz NULL,
  created_at     timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT transcript_read_status
    CHECK (status IN ('queued','running','done','failed')),

  -- A terminal row has finished, a live one has not. Without this a read can
  -- claim to be running while carrying a finish time, and the surface that
  -- renders it would have to pick which half to believe.
  CONSTRAINT transcript_read_terminal_shape CHECK (
    (status IN ('done','failed') AND finished_at IS NOT NULL)
    OR
    (status IN ('queued','running') AND finished_at IS NULL)
  ),
  -- Only a read that has been claimed has a start time.
  CONSTRAINT transcript_read_started_shape CHECK (
    (status = 'queued' AND started_at IS NULL)
    OR
    (status <> 'queued' AND started_at IS NOT NULL)
  )
);

-- One live read per transcript. Pressing the button twice joins the read
-- already in flight instead of paying for the same transcript twice and
-- staging every proposal in duplicate. Partial, so the history of finished
-- reads is unconstrained — a transcript may honestly be re-read after an edit.
CREATE UNIQUE INDEX uq_transcript_read_inflight
  ON transcript_read (activity_id)
  WHERE status IN ('queued','running');

-- The surface's own query: the latest read of the transcript on screen.
CREATE INDEX idx_transcript_read_latest
  ON transcript_read (workspace_id, activity_id, created_at DESC);
