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
-- predicate, because the four old values remain. No backfill, no rewrite. The
-- ALTER still needs a brief ACCESS EXCLUSIVE lock to swap the constraint, and
-- audit_log takes a row from every mutation in the product — so the wait is
-- bounded rather than left to queue behind whatever transaction is open.
SET LOCAL lock_timeout = '3s';

ALTER TABLE audit_log DROP CONSTRAINT audit_log_actor_type_check;
ALTER TABLE audit_log ADD CONSTRAINT audit_log_actor_type_check
    CHECK (actor_type IN ('human', 'agent', 'connector', 'system', 'buyer'));

-- system_log takes the same widening for one reason only: the two ledgers
-- share an actor vocabulary, and a kind valid in one and refused by the other
-- is a trap for the next writer rather than a boundary anybody chose. A buyer
-- does not write system_log today — that ledger records operational acts that
-- mutate no record, and a buyer performs none.
ALTER TABLE system_log DROP CONSTRAINT system_log_actor_type_check;
ALTER TABLE system_log ADD CONSTRAINT system_log_actor_type_check
    CHECK (actor_type IN ('human', 'agent', 'connector', 'system', 'buyer'));
