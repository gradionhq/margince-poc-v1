-- The ledgers learn a fifth actor kind: `buyer`, an external person acting
-- inside one Deal Room.
--
-- WHY A NEW KIND RATHER THAN REUSING ONE. A Deal Room participant holds no
-- seat, so `human` is wrong twice over — it means a member, and every reader of
-- an audit row resolves it against the member directory, where a buyer will
-- never appear. `system` is worse: it would render "System confirmed v5" over
-- exactly the question a disputed negotiation asks, and audit_log is append-only,
-- so that reading could never be corrected. The alternative was to keep
-- actor_type='system' and bury the participant in the evidence blob, which
-- leaves every reader to know to look there and the audit screen still drawing
-- a Cog icon beside a person's decision.
--
-- Widening a CHECK is additive: every row already stored satisfies the new
-- predicate, because the four old values remain. No backfill, no rewrite.
--
-- It is still done in two steps rather than one, and the reason is a trap worth
-- naming. A plain ADD CONSTRAINT validates every existing row WHILE HOLDING
-- ACCESS EXCLUSIVE, and `lock_timeout` does not help: it bounds how long the
-- statement waits to ACQUIRE the lock, never how long it holds one once it has
-- it. On a mature audit_log — a row for every mutation the product has ever
-- made — that scan is the outage. NOT VALID skips the scan and takes the lock
-- only long enough to record the constraint; VALIDATE then checks the existing
-- rows under a SHARE UPDATE EXCLUSIVE lock that readers and writers pass.
--
-- The validation cannot fail here, because the new predicate admits everything
-- the old one did. It is run anyway rather than left NOT VALID: an unvalidated
-- constraint is not trusted by the planner and reads as provisional to the next
-- person who looks at the schema.
SET LOCAL lock_timeout = '3s';

ALTER TABLE audit_log ADD CONSTRAINT audit_log_actor_type_check_v2
    CHECK (actor_type IN ('human', 'agent', 'connector', 'system', 'buyer')) NOT VALID;
ALTER TABLE audit_log VALIDATE CONSTRAINT audit_log_actor_type_check_v2;
ALTER TABLE audit_log DROP CONSTRAINT audit_log_actor_type_check;
ALTER TABLE audit_log RENAME CONSTRAINT audit_log_actor_type_check_v2 TO audit_log_actor_type_check;

-- system_log takes the same widening for one reason only: the two ledgers
-- share an actor vocabulary, and a kind valid in one and refused by the other
-- is a trap for the next writer rather than a boundary anybody chose. A buyer
-- does not write system_log today — that ledger records operational acts that
-- mutate no record, and a buyer performs none.
ALTER TABLE system_log ADD CONSTRAINT system_log_actor_type_check_v2
    CHECK (actor_type IN ('human', 'agent', 'connector', 'system', 'buyer')) NOT VALID;
ALTER TABLE system_log VALIDATE CONSTRAINT system_log_actor_type_check_v2;
ALTER TABLE system_log DROP CONSTRAINT system_log_actor_type_check;
ALTER TABLE system_log RENAME CONSTRAINT system_log_actor_type_check_v2 TO system_log_actor_type_check;
