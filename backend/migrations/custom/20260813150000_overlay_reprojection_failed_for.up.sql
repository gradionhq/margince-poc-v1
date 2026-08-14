-- A re-projection re-fetches a record live and re-ingests it. When the
-- incumbent still holds the record but this build cannot map it, the read
-- fails in a way a retry cannot change and the row keeps its old projection —
-- so the next sweep pass names it again, spending one incumbent read per tick
-- forever while the flip stays blocked on it.
--
-- This records WHICH declaration the row failed to reach, not merely that it
-- failed: a repaired declaration has a different fingerprint, so the record
-- stops matching and the row is retried with no operator action.
ALTER TABLE overlay_mirror ADD COLUMN reprojection_failed_for text;
