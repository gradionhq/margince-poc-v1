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
ALTER TABLE attachment_extraction
  ADD COLUMN attempt integer NOT NULL DEFAULT 1 CHECK (attempt >= 1);
