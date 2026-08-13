-- Reverting to 0104's shape, where only a deferred read may carry
-- status_code/status_detail/next_attempt_at.
--
-- The diagnoses recorded under 0221 have to go: the old constraint forbids them
-- on a failed row, so keeping them would make the table unrestorable. That is a
-- real loss of information, and it is the price of the revert — the failures
-- themselves are preserved, only the reason they failed is dropped.

-- Order matters, and getting it wrong makes the rollback impossible: the new
-- CHECK requires a failed row to NAME its cause, so nulling those columns while
-- it is still active violates it on every diagnosed failure — including the rows
-- the up migration backfilled. The constraint comes off FIRST, and the old one
-- goes on only once the data fits it again.
ALTER TABLE site_read DROP CONSTRAINT site_read_outcome_shape;

DO $$
DECLARE ws uuid;
BEGIN
  FOR ws IN SELECT id FROM workspace LOOP
    PERFORM set_config('app.workspace_id', ws::text, true);

    UPDATE site_read
       SET status_code = NULL, status_detail = NULL, next_attempt_at = NULL
     WHERE status <> 'deferred'
       AND (status_code IS NOT NULL OR status_detail IS NOT NULL OR next_attempt_at IS NOT NULL)
       AND site_read.workspace_id = ws;
  END LOOP;
END $$;

ALTER TABLE site_read
  ADD CONSTRAINT site_read_deferral_shape CHECK (
    (status = 'deferred' AND status_code = 'budget_deferred' AND
      status_detail IS NOT NULL AND next_attempt_at IS NOT NULL) OR
    (status <> 'deferred' AND status_code IS NULL AND
      status_detail IS NULL AND next_attempt_at IS NULL)
  );

DROP INDEX idx_site_read_retry_due;
CREATE INDEX idx_site_read_deferred_due
  ON site_read (workspace_id, next_attempt_at, id)
  WHERE status = 'deferred';
