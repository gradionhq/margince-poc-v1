-- Narrowing the vocabulary again is NOT symmetric with widening it, and the
-- difference matters enough to state rather than discover.
--
-- Widening admitted every row already stored. Narrowing refuses any row written
-- since — and audit_log is append-only by design, so there is no honest repair:
-- rewriting a buyer's action as `system` would falsify who acted, and deleting
-- the row would destroy the record the ledger exists to keep. Postgres will
-- therefore refuse this migration on an installation where a buyer has ever
-- acted, which is the correct outcome: the down path exists to undo a mistake
-- made before the feature was used, not to erase history after it was.
--
-- An operator who hits that refusal is not stuck for want of a clever query.
-- They are being told that going back means losing attribution somebody may
-- need, and the decision is theirs to make deliberately.
SET LOCAL lock_timeout = '3s';

ALTER TABLE audit_log DROP CONSTRAINT audit_log_actor_type_check;
ALTER TABLE audit_log ADD CONSTRAINT audit_log_actor_type_check
    CHECK (actor_type IN ('human', 'agent', 'connector', 'system'));

ALTER TABLE system_log DROP CONSTRAINT system_log_actor_type_check;
ALTER TABLE system_log ADD CONSTRAINT system_log_actor_type_check
    CHECK (actor_type IN ('human', 'agent', 'connector', 'system'));
