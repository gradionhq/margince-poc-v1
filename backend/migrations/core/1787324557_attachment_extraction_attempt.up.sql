-- Three columns and two indexes the AI-activity projection needs from the
-- SOURCE, because each of them is a fact only the source can hold.
--
-- ACCESS EXCLUSIVE on a table this migration did not create. The change itself
-- is instant — no row is rewritten — but the lock still queues behind every
-- open transaction on the table, and an unbounded wait turns one long-running
-- reader into a total write stall. Three seconds, so a migration that cannot
-- get in fails the deploy loudly instead of holding the door.
SET LOCAL lock_timeout = '3s';

-- Which claim of this reading is current, and when it became current.
--
-- The reading's lifecycle is not monotonic: a claimed row can be released by a
-- retrying worker, re-armed after its lease expires, or RECLAIMED in place by a
-- retry that finds the previous claim dead. Every one of those begins a new
-- attempt at the same reading. Nothing needed to count them while the row was
-- only ever read as "what is it doing now" — the status answered that alone.
--
-- The projection needs the count, because it orders two events for one
-- occurrence and status cannot: a 'queued' that supersedes a 'running' and a
-- 'queued' that is a stale redelivery of an earlier one look identical without
-- it. It lives HERE rather than as a counter the projection keeps, because a
-- claim's identity is the source's fact — a second truth about which claim is
-- current is the thing that goes wrong.
--
-- attempt_at is the other half and is not decoration: the projection ages a
-- LIVE occurrence from the instant its current attempt began, and created_at is
-- that instant only for the first one. A re-armed reading dated by created_at
-- is past its lease the moment it is re-queued — the row would render as
-- stalled from the button press, which is the display this whole projection
-- exists to prevent.
ALTER TABLE attachment_extraction
  ADD COLUMN attempt    integer     NOT NULL DEFAULT 1 CHECK (attempt >= 1),
  ADD COLUMN attempt_at timestamptz NOT NULL DEFAULT now();

-- When the reconcile pass last re-announced this reading to the projection.
--
-- It is the pass's ROTATION KEY, and without it the pass makes no progress: a
-- bounded batch ordered by any column an announce does not change selects the
-- same rows every tick forever, so a reading past the batch bound is never
-- reconciled at all and a permanently-live one writes a ledger row every tick
-- for an announcement the projection's guard then refuses.
ALTER TABLE attachment_extraction
  ADD COLUMN activity_announced_at timestamptz NULL;

-- The reconcile pass's two reads, one index each. Its predicate is a UNION of
-- two arms rather than an OR precisely so both can be indexed: an OR over
-- `status IN (...)` and `finished_at > $1` can use neither.
CREATE INDEX idx_attachment_extraction_activity_live
  ON attachment_extraction (activity_announced_at ASC NULLS FIRST)
  WHERE status IN ('queued','running');

CREATE INDEX idx_attachment_extraction_activity_settled
  ON attachment_extraction (finished_at DESC)
  WHERE status IN ('done','failed');
