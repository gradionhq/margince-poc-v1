-- Which claim of this reading is current.
--
-- The reading's lifecycle is not monotonic: a claimed row can be released by a
-- retrying worker, or re-armed after its lease expires, and then claimed again.
-- Every one of those is a NEW attempt at the same reading. Nothing needed to
-- count them while the row was only ever read as "what is it doing now" — the
-- status answered that on its own.
--
-- The AI-activity projection needs the count, because it orders two events for
-- one occurrence and status alone cannot: a 'queued' that supersedes a
-- 'running' and a 'queued' that is a stale redelivery of an earlier one look
-- identical without it. It lives HERE, on the source, rather than as a counter
-- the projection keeps, because a claim's identity is the source's fact — a
-- second truth about which claim is current is the thing that goes wrong.
-- ACCESS EXCLUSIVE on a table this migration did not create. The change itself
-- is instant — no row is rewritten — but the lock still queues behind every
-- open transaction on the table, and an unbounded wait turns one long-running
-- reader into a total write stall. Three seconds, so a migration that cannot
-- get in fails the deploy loudly instead of holding the door.
SET LOCAL lock_timeout = '3s';

ALTER TABLE attachment_extraction
  ADD COLUMN attempt integer NOT NULL DEFAULT 1 CHECK (attempt >= 1);
