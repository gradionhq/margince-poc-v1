-- 0074: workflow_run.detail (Schema appendix pins `detail jsonb`). The
-- existing `error` text column carries two payloads: a human-readable
-- failure/skip/block reason, and — while a run is parked for a staged
-- 🟡 approval — a machine-parsed staging pointer matched back by exact
-- string equality (workflows_blocked.go). Encoding a machine pointer as
-- a bare string that must be string-matched is fragile; a jsonb object
-- lets the pointer carry the approval id as a real field a matcher
-- queries structurally, with the reason staying human-readable alongside
-- it. `error` stays untouched (a follow-up drops it once nothing reads
-- it) so no reader breaks mid-migration.
ALTER TABLE workflow_run ADD COLUMN detail jsonb NULL;

-- Backfill: every existing reason wraps into {"reason": <error>}. A
-- 'requires_approval' row whose error still carries the pre-migration
-- staging-pointer sentence gets its approval id parsed out into
-- approval_id too, so a rejection landing after this migration still
-- finds and blocks the run it parked before the migration ran — the
-- backfilled shape and the shape runOne/MarkRunBlocked write from here
-- on are read through the same decoder (rundetail.go) either way.
UPDATE workflow_run
SET detail = CASE
  WHEN error IS NULL THEN NULL
  WHEN status = 'requires_approval'
       AND error ~ '^staged as approval [0-9a-fA-F-]{36}; awaiting the human decision$'
    THEN jsonb_build_object(
      'reason', error,
      'approval_id', substring(error from 'staged as approval ([0-9a-fA-F-]{36})')
    )
  ELSE jsonb_build_object('reason', error)
END;
