-- A held record is out of the default lists by construction
-- (A165/ADR-0114 §2).
--
-- The restrict step archives the row it holds, and the guard refuses any
-- later write that would un-archive it while it is held. Most readers of
-- activity filter `archived_at IS NULL`, and A165 promises that a restricted
-- record is unavailable in EVERY ordinary read path — so those readers are
-- correct only while "restricted implies archived" holds. Stated as a CHECK
-- rather than left to the two writers, because a rule two writers keep by
-- convention is one the third writer breaks; the readers relying on it are
-- named by the restricted-readers fitness test.
ALTER TABLE activity
  ADD CONSTRAINT activity_restricted_is_archived
    CHECK (restricted_at IS NULL OR archived_at IS NOT NULL);
