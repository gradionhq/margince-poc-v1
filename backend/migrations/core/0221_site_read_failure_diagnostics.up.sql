-- 0221: a failed website read says WHY it failed, and whether it is worth
-- another attempt.
--
-- 0104 gave site_read status_code/status_detail/next_attempt_at and then
-- reserved all three for status='deferred', because budget deferral was the
-- only non-terminal outcome that existed. Every other failure was recorded as
-- a bare status='failed' with the three columns NULL — so a TLS error, a DNS
-- miss, a robots refusal and an edge 403 were stored identically, as nothing.
-- In a real import that produced 58 failed reads, all 58 with no diagnosis and
-- no scheduled retry, and the companies behind them kept an empty record
-- nobody could see the reason for.
--
-- The shape now says: deferred keeps its exact budget spelling; a failed read
-- MUST name a cause and MAY carry a retry time; done/partial/cancelled carry
-- none of the three. status_code is a closed vocabulary rather than free text
-- so an operator can group by it, and status_detail is the one sentence a
-- human reads.

ALTER TABLE site_read DROP CONSTRAINT site_read_deferral_shape;

-- Reads that already failed under the old shape carry no diagnosis, and none
-- can be recovered — the worker discarded the cause before writing the row.
-- They are labelled for exactly that rather than guessed at, because a made-up
-- code would be indistinguishable from a measured one and would poison the
-- first honest grouping an operator does. No retry time is set: nothing here
-- says whether another attempt would help.
--
-- The predicate names its own target rather than every failed row, so re-running
-- this on a database where the new writer has already recorded real causes
-- cannot overwrite them.
-- The per-workspace loop: site_read is a tenant table, so the write binds
-- app.workspace_id per workspace and scopes the statement to that workspace by
-- predicate. The binding makes the rows visible under row-level security; the
-- predicate is what keeps a BYPASSRLS executor (every dev machine and CI) from
-- re-running the same update once per workspace.
DO $$
DECLARE ws uuid;
BEGIN
  FOR ws IN SELECT id FROM workspace LOOP
    PERFORM set_config('app.workspace_id', ws::text, true);

    UPDATE site_read
       SET status_code = 'unreadable',
           status_detail = 'Recorded before failures carried a diagnosis; the original cause was not stored.'
     WHERE status = 'failed'
       AND (status_code IS NULL OR status_detail IS NULL)
       AND site_read.workspace_id = ws;
  END LOOP;
END $$;

ALTER TABLE site_read
  ADD CONSTRAINT site_read_outcome_shape CHECK (
    (status = 'deferred' AND status_code = 'budget_deferred' AND
      status_detail IS NOT NULL AND next_attempt_at IS NOT NULL)
    OR
    -- A failure names its cause. next_attempt_at is nullable here on purpose:
    -- it is set for the causes worth retrying (a 403 from bot protection, a
    -- 5xx, a timeout) and left NULL for the ones that will not change on their
    -- own (a robots refusal, a domain that does not resolve).
    (status = 'failed' AND status_code IN (
        'bot_blocked', 'http_client_error', 'http_server_error', 'timeout',
        'dns', 'tls', 'robots_disallowed', 'unreadable', 'internal')
      AND status_detail IS NOT NULL)
    OR
    (status IN ('queued', 'running', 'done', 'partial', 'cancelled') AND
      status_code IS NULL AND status_detail IS NULL AND next_attempt_at IS NULL)
  );

-- The due-retry index covers failed reads now, not just deferred ones: both are
-- rows a sweep must find again at a time the row itself names.
DROP INDEX idx_site_read_deferred_due;
CREATE INDEX idx_site_read_retry_due
  ON site_read (workspace_id, next_attempt_at, id)
  WHERE status IN ('deferred', 'failed') AND next_attempt_at IS NOT NULL;
