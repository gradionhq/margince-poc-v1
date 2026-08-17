-- 0288: an activity records the statutory class it earned, and the restriction
-- that class produces (A165/ADR-0114, A167/ADR-0116).
--
-- Two jobs in one table, and they are written at different times by different
-- code, so the constraints are what keep them coherent rather than convention.
--
-- retention_class is the MONOTONIC stamp. It is written when the row first
-- EARNS the class — its deal is won, an offer on that deal leaves draft, or it
-- is linked to a deal that already qualifies — and never re-derived. That is
-- the load-bearing half of A165: qualification is REVERSIBLE in the product (a
-- won deal can be reopened, and relinking an activity deletes its existing link
-- of that type), so a rule that asks the question at erasure time asks it of a
-- record whose evidence may have moved. The failure is not symmetric —
-- over-retention is an argument to have with a supervisory authority, and
-- destruction is irreversible.
--
-- The other three are the RESTRICTION state, written when an erasure meets a
-- stamped row: the record leaves every ordinary read path instead of being
-- destroyed, and carries the deadline at which its obligation ends. The
-- deadline is PINNED here, from the floor in force at that moment, so a later
-- change to a configured period never shortens an obligation already recorded.
ALTER TABLE activity
  ADD COLUMN retention_class     text NULL,
  ADD COLUMN retention_class_at  timestamptz NULL,
  ADD COLUMN restricted_at       timestamptz NULL,
  ADD COLUMN restricted_reason   text NULL,
  ADD COLUMN restricted_until    timestamptz NULL,
  -- Which fields an erasure emptied on this row. A redacted field and an empty
  -- one are otherwise the same NULL, and only the first is a statement the
  -- controller must be able to make to the subject. NOT NULL DEFAULT '{}'
  -- because "no redactions" is a real state that must not be spelled the same
  -- way as "unknown".
  ADD COLUMN redacted_fields     text[] NOT NULL DEFAULT '{}';

ALTER TABLE activity
  ADD CONSTRAINT activity_retention_class_known
    CHECK (retention_class IS NULL OR retention_class IN ('commercial_correspondence')),
  -- The stamp carries its own timestamp or neither is trustworthy.
  ADD CONSTRAINT activity_retention_class_stamped
    CHECK ((retention_class IS NULL) = (retention_class_at IS NULL)),
  -- All-or-none. A row with restricted_at set and restricted_until NULL is
  -- never selected by the expiry sweep, which reads `restricted_until <= now()`
  -- — hidden from every read path, immutable, and never erased. A permanently
  -- invisible record, produced by one forgotten assignment.
  ADD CONSTRAINT activity_restriction_complete
    CHECK ((restricted_at IS NULL AND restricted_reason IS NULL AND restricted_until IS NULL)
        OR (restricted_at IS NOT NULL AND restricted_reason IS NOT NULL AND restricted_until IS NOT NULL)),
  -- A window that closed before it opened erases immediately, which looks
  -- exactly like the feature working.
  ADD CONSTRAINT activity_restriction_window
    CHECK (restricted_until IS NULL OR restricted_until > restricted_at),
  -- The stamp is what the obligation rests on; a restriction without one is an
  -- assertion with nothing behind it.
  ADD CONSTRAINT activity_restriction_needs_class
    CHECK (restricted_at IS NULL OR retention_class IS NOT NULL);

-- The expiry sweep's working set: restricted rows whose window has run out.
-- The predicate matches the sweep's own WHERE exactly, and leading on the
-- deadline keeps the due rows at one end rather than scanning every restricted
-- row to find them.
CREATE INDEX idx_activity_restricted_until
  ON activity (restricted_until) WHERE restricted_at IS NOT NULL;
