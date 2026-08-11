-- 0003: the note table's one index (ADR-0069).
--
-- A NEW migration rather than a line added to 0001, and that is the rule
-- rather than a preference here: 0001 is already recorded as applied on every
-- installation that ever ran it, so a line added to it is a line that runs on
-- exactly the installations that did not need it — a fresh one — and never on
-- the ones that do. An upgrade is a new number.
--
-- The index earns its place twice. The table is cross-tenant, so every read the
-- policy admits still has to FIND this workspace's rows among every other
-- workspace's; leading on workspace_id turns the list into a range scan, and
-- trailing on created_at DESC is the order listNotes asks for, so the sort
-- comes free. The leading column is also what the workspace cascade needs:
-- without an index beginning with the referencing column, deleting one
-- workspace sequentially scans this whole table once per row it removes.
CREATE INDEX ext_notes_note_workspace_created
    ON ext.ext_notes_note (workspace_id, created_at DESC);
