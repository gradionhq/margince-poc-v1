SET LOCAL lock_timeout = '3s';

ALTER TABLE capture_trace DROP CONSTRAINT capture_trace_stage_outcome_check;
ALTER TABLE capture_trace ADD CONSTRAINT capture_trace_stage_outcome_check CHECK (
     (stage = 'internal_drop'  AND outcome = 'internal')
  OR (stage = 'activity_write' AND outcome = 'fault')
  OR (stage = 'tier_ladder'    AND outcome IN ('captured', 'suppressed', 'deferred', 'fault'))
);
DROP TABLE IF EXISTS capture_exclusion;
