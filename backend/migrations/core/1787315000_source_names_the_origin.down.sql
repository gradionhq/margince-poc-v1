-- Irreversible by design: `mcp`, `ui` and `manual` are one word now, and the
-- row no longer carries which of the three it was. `captured_by` does.
--
-- A down migration that guessed would be worse than one that does nothing.
SELECT 1;
