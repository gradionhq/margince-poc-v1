-- Reverse of 0271, in reverse order.
--
-- The stage column is dropped rather than preserved, so the rows that survive
-- are exactly 0258's shape. That loses which step explained each row, which is
-- the point of going down: a binary that does not know about stages must not
-- meet a table that insists on one.
--
-- One row per message per outcome is restored as the dedupe rule. Where two
-- stages recorded the same (message, outcome) pair -- only the two fault stages
-- can -- the older row wins and the newer is discarded, because the restored
-- unique index cannot hold both. That is a real loss and it is the honest
-- consequence of narrowing the key; a diagnostic row whose whole lifetime is 24
-- hours is the right thing to lose here rather than failing the migration.

DROP INDEX capture_trace_message;

DELETE FROM capture_trace loser USING capture_trace keeper
 WHERE loser.workspace_id = keeper.workspace_id
   AND COALESCE(loser.user_id, '00000000-0000-0000-0000-000000000000'::uuid)
     = COALESCE(keeper.user_id, '00000000-0000-0000-0000-000000000000'::uuid)
   AND loser.source_system = keeper.source_system
   AND loser.source_id = keeper.source_id
   AND loser.outcome = keeper.outcome
   -- uuidv7 is time-ordered, so the LARGER id is the newer row: the keeper is
   -- the one recorded first.
   AND loser.id > keeper.id;

DROP INDEX capture_trace_natural_key;
CREATE UNIQUE INDEX capture_trace_natural_key ON capture_trace
  (workspace_id, COALESCE(user_id, '00000000-0000-0000-0000-000000000000'::uuid),
   source_system, source_id, outcome);

ALTER TABLE capture_trace DROP CONSTRAINT capture_trace_stage_outcome_check;
ALTER TABLE capture_trace ADD CONSTRAINT capture_trace_outcome_check CHECK (
  outcome IN ('captured', 'internal', 'suppressed', 'deferred', 'fault')
);

ALTER TABLE capture_trace DROP COLUMN stage;
