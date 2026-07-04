-- 0032: the double-booking guarantee moves into the database. The
-- read-then-write probe in the booking path cannot survive two
-- concurrent transactions; an exclusion constraint over the assumed
-- 1-hour meeting window can. The application maps 23P01 on this
-- constraint to the contract's 409 slot_taken. Ranges are built over
-- timezone('UTC', …) timestamps: timestamptz arithmetic is only STABLE
-- (session-timezone-dependent), while timezone(text, timestamptz) and
-- plain-timestamp + interval are IMMUTABLE as index expressions demand.
CREATE EXTENSION IF NOT EXISTS btree_gist;

ALTER TABLE activity
  ADD CONSTRAINT activity_meeting_no_overlap EXCLUDE USING gist (
    workspace_id WITH =,
    host_user_id WITH =,
    tsrange(timezone('UTC', occurred_at),
            timezone('UTC', occurred_at) + interval '1 hour') WITH &&
  ) WHERE (kind = 'meeting' AND host_user_id IS NOT NULL AND archived_at IS NULL);
