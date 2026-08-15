-- 0251: one reading of one attached document, and what it grounded.
--
-- RD-DDL-4. Reading a document is a model call that takes seconds and can
-- fail, so it cannot happen inside the request that asks for it. This row is
-- what makes the asking honest: the POST answers 202 with its id, and the
-- client polls it until it is terminal. Deep read (`site_read`, 0085) and the
-- transcript reading (`transcript_read`, 0245) are the same shape and the same
-- reason.
--
-- The row exists so three answers can be told apart, which is the whole point
-- of showing it to a human:
--
--   running         the model has not answered yet
--   done, 0 fields  it read the document and could ground none of the four
--                   fields in it — a CORRECT empty answer under GATE-AI-1
--   failed          it could not read the document at all
--
-- Collapsing the last two is the failure mode this feature must not have: an
-- order form that simply never states a close date is not a broken reading, and
-- a rep who cannot tell those apart will either distrust a good answer or trust
-- a broken one.
--
-- What it deliberately is NOT: an approval. The transcript reading stages its
-- proposals as approval rows, each its own authority object with its own diff
-- hash and expiry (ADR-0036), so that table stores no result. A document
-- reading stages nothing — the accept is a human's own direct call on the
-- attachment surface (`x-agent-access: human-only`) — so this row IS where the
-- grounded fields live between the reading and the accept.
--
-- That is not an optimisation. Without it the accept has nothing to validate a
-- human's choice against except a SECOND reading of the same document, and two
-- readings of one document are not guaranteed to agree: the human accepts the
-- amount they were shown and a different one lands on the deal, carrying an
-- audit note that quotes evidence they never saw (RD-AC-N-5).
--
-- No workspace_id and no RLS, matching every table added since ADR-0091: one
-- installation serves one organization (A107), so there is no second tenant a
-- policy could isolate a reading from. What protects it is the attachment's own
-- row-scope gate, taken on every path that reaches it.
CREATE TABLE attachment_extraction (
  id             uuid PRIMARY KEY DEFAULT uuidv7(),

  -- CASCADE: a reading of a deleted attachment has nothing left to be a
  -- reading OF, and nothing it produced outlives it — unlike a transcript
  -- reading, whose proposals are approval rows carrying their own evidence
  -- precisely so a citation stays readable when the run record is gone.
  attachment_id  uuid NOT NULL REFERENCES attachment(id) ON DELETE CASCADE,

  status         text NOT NULL DEFAULT 'queued',

  -- Why it ended the way it did, in words a rep can act on. Set on failure;
  -- also set on a done-but-empty reading, to say the document states none of
  -- the four fields rather than leaving an empty list to explain itself.
  status_detail  text NULL,

  -- What the reading grounded and what it honestly omitted, in the port's own
  -- shape. jsonb rather than a child table: a field is only ever read back as
  -- part of the whole reading, never queried across readings, and a child
  -- table would invite exactly the cross-reading query that would make a
  -- superseded reading's field look current.
  fields         jsonb NOT NULL DEFAULT '[]'::jsonb,

  requested_by   text NOT NULL,
  started_at     timestamptz NULL,
  finished_at    timestamptz NULL,
  created_at     timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT attachment_extraction_status
    CHECK (status IN ('queued','running','done','failed')),

  -- fields is a list. Without this a reading can store an object or a scalar
  -- there and every reader has to defend against a shape the writer never
  -- meant to produce.
  CONSTRAINT attachment_extraction_fields_shape
    CHECK (jsonb_typeof(fields) = 'array'),

  -- A terminal reading has finished, a live one has not. Without this a row can
  -- claim to be running while carrying a finish time, and the surface that
  -- renders it would have to pick which half to believe.
  CONSTRAINT attachment_extraction_terminal_shape CHECK (
    (status IN ('done','failed') AND finished_at IS NOT NULL)
    OR
    (status IN ('queued','running') AND finished_at IS NULL)
  ),
  -- Only a reading that has been claimed has a start time.
  CONSTRAINT attachment_extraction_started_shape CHECK (
    (status = 'queued' AND started_at IS NULL)
    OR
    (status <> 'queued' AND started_at IS NOT NULL)
  )
);

-- One live reading per attachment. Pressing the button twice joins the reading
-- already in flight instead of paying for the same document twice. Partial, so
-- the history of finished readings is unconstrained — a document may honestly
-- be read again after a failure, and the newest reading is the one its surface
-- shows.
CREATE UNIQUE INDEX uq_attachment_extraction_inflight
  ON attachment_extraction (attachment_id)
  WHERE status IN ('queued','running');

-- The surface's own query: the latest reading of the attachment on screen.
CREATE INDEX idx_attachment_extraction_latest
  ON attachment_extraction (attachment_id, created_at DESC);
