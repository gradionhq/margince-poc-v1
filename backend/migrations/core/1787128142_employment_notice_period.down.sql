-- Nothing. The forward direction restores a flag that was wrongly cleared, and
-- the rows it touched are indistinguishable from rows that always held it — a
-- reverse would have to strip the flag from people who never lost it. A down
-- migration that destroys correct data to undo a repair is worse than one that
-- leaves the repair standing.
SELECT 1;
