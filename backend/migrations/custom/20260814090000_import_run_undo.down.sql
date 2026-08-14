ALTER TABLE import_run DROP COLUMN undo_report;

-- A row left mid-reversal or already undone has no home in the narrower
-- CHECK this rollback restores; folding it back to 'complete' is honest
-- (the row's own domain effects are whatever they were at rollback time)
-- rather than leaving the rollback unable to run at all. checkpoint resets
-- to 0 in the same statement: it was the UNDO pass's own cursor, not the
-- forward commit's, and leaving it non-zero on a row now claiming
-- 'complete' would misrepresent the forward run as interrupted.
UPDATE import_run SET status = 'complete', checkpoint = 0 WHERE status IN ('undoing', 'undone');

ALTER TABLE import_run DROP CONSTRAINT IF EXISTS import_run_status_check;
ALTER TABLE import_run ADD CONSTRAINT import_run_status_check
  CHECK (status IN ('pending','validating','awaiting_approval','running','complete','failed'));
