ALTER TABLE import_run DROP COLUMN undo_report;

-- A row left mid-reversal or already undone has no home in the narrower
-- CHECK this rollback restores; folding it back to 'complete' is honest
-- (the row's own domain effects are whatever they were at rollback time)
-- rather than leaving the rollback unable to run at all.
UPDATE import_run SET status = 'complete' WHERE status IN ('undoing', 'undone');

ALTER TABLE import_run DROP CONSTRAINT IF EXISTS import_run_status_check;
ALTER TABLE import_run ADD CONSTRAINT import_run_status_check
  CHECK (status IN ('pending','validating','awaiting_approval','running','complete','failed'));
