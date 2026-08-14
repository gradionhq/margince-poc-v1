-- 20260814090000_import_run_undo: the reversal lifecycle for a committed CSV
-- import run (IEM-WIRE-9; A93; S-E15.4c). Fork-owned migration (ADR-0017
-- custom namespace) — import_run is the migration module's own table.
--
-- Two new terminal-adjacent states: 'undoing' while the reversal walks
-- import_record_map (resumable on the run's existing checkpoint column,
-- the same shape the forward commit already uses), 'undone' once it
-- finishes. Reachable only from 'complete', and only for the csv
-- connector — enforced in Go (RunStore.Undo), not by a second CHECK here,
-- because the rule spans two columns (status AND connector).
ALTER TABLE import_run DROP CONSTRAINT IF EXISTS import_run_status_check;
ALTER TABLE import_run ADD CONSTRAINT import_run_status_check
  CHECK (status IN ('pending','validating','awaiting_approval','running','complete','failed','undoing','undone'));

-- The undo outcome: reversed_count + the "kept — you edited these" list
-- (A93). Separate from `report` (the dry-run/commit report) so undoing a
-- run never overwrites what it originally did — an operator comparing the
-- two needs both, not one clobbering the other. Also carries the
-- reference instant ("since") the human-edit check reads, captured once
-- when the reversal starts so a resumed attempt reads the same instant
-- rather than a moved goalpost.
ALTER TABLE import_run ADD COLUMN undo_report jsonb NULL;
