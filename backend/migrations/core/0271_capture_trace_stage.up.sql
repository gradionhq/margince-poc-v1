-- 0271: which PIPELINE STAGE a capture_trace row explains.
--
-- 0258 gave a member the outcomes of capture. It could not tell them which STEP
-- produced one, and it had nothing to say about the steps that leave no trace
-- row at all -- the attention classifier reads `kind = 'email'`, so a message
-- from a chat transport is never eligible, and no surface said so. Answering
-- that needs the rungs named, and this column is the naming.
--
-- Only three stages are STORED, because only three write anything nothing else
-- records. Every other stage is answered from durable product state the pipeline
-- already keeps -- a copy would be a second source that can disagree with the
-- first, which is the reason 0258 gives for never copying verdict outcomes here.
--
-- DEPLOY ORDER MATTERS MORE THAN USUAL. After SET NOT NULL an old binary's
-- INSERT omits the column and fails, and per 0258 a trace error on the capture
-- transaction fails the CAPTURE. Nothing is lost -- a connector sees an error
-- rather than a skip, so its cursor holds and it retries -- but this is
-- migrate-then-restart, not the reverse.

ALTER TABLE capture_trace ADD COLUMN stage text;

-- Backfill by what each row MEANS, never a flat default.
--
-- An `internal` row IS the internal-only drop: that gate returns before the tier
-- ladder ever runs, so stamping it `tier_ladder` would both mislabel it and make
-- the funnel's `internal` counter -- which is now derived per stage -- undercount
-- for the life of the window.
--
-- An `invisible_incumbent` fault is the ACTIVITY WRITE refusing a replay whose
-- incumbent row sits outside the reader's scope (capture/sinktrace.go). It is a
-- capture-transaction fault, not a ladder decision, and it is the only fault
-- that is not the ladder's.
--
-- Writer census behind the ELSE arm: `internal` has exactly one writer
-- (capture/sink.go, the internal gate), `invisible_incumbent` exactly one
-- (capture/sinktrace.go), and every remaining writer goes through traceActivity
-- with a ladder outcome. No fault reaches the ELSE arm without a reason naming
-- the ladder, so nothing lands there by omission.
UPDATE capture_trace SET stage = CASE
  WHEN outcome = 'internal' THEN 'internal_drop'
  WHEN outcome = 'fault' AND reason = 'invisible_incumbent' THEN 'activity_write'
  ELSE 'tier_ladder'
END;

ALTER TABLE capture_trace ALTER COLUMN stage SET NOT NULL;

-- Per-stage, not a flat union of both vocabularies. A union would admit cross
-- products that no writer can produce -- an `internal_drop` row carrying
-- `deferred` -- and the column would then be unable to say which combinations
-- are real. The stage list here is asserted equal to the registry's stored
-- stages by a fitness test, so a constraint can never admit a stage the product
-- has never heard of.
ALTER TABLE capture_trace DROP CONSTRAINT capture_trace_outcome_check;
ALTER TABLE capture_trace ADD CONSTRAINT capture_trace_stage_outcome_check CHECK (
     (stage = 'internal_drop'  AND outcome = 'internal')
  OR (stage = 'activity_write' AND outcome = 'fault')
  OR (stage = 'tier_ladder'    AND outcome IN ('captured', 'suppressed', 'deferred', 'fault'))
);

-- The dedupe key gains the stage dimension.
--
-- 0258's reason for this index is unchanged and still load-bearing: the internal
-- gate fires BEFORE the dedupe upsert, so without it a re-walked region counts
-- one colleague message once per poll and the funnel measures polling rather
-- than mail. Adding `stage` keeps that protection PER STAGE rather than across
-- stages, so two different steps may each explain the same message -- which is
-- the point of the column -- while neither can record itself twice.
DROP INDEX capture_trace_natural_key;
CREATE UNIQUE INDEX capture_trace_natural_key ON capture_trace
  (workspace_id, COALESCE(user_id, '00000000-0000-0000-0000-000000000000'::uuid),
   source_system, source_id, stage, outcome);

-- The ladder reads one message's rungs in order. Without this it is a scan of
-- the member's whole window to assemble a single drill-down.
CREATE INDEX capture_trace_message ON capture_trace
  (workspace_id, source_system, source_id);
