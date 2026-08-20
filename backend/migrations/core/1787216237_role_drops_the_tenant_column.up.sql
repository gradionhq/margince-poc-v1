-- ADR-0091 §8 phase D: role and role_assignment drop the tenant column.
--
-- A single-organization installation has one workspace (ADR-0061), so every
-- role and every assignment already belonged to it; `key` is unique across the
-- table without the column, and no read predicate names it.
--
-- uq_role_ws_id goes with it. It is a phase B leftover — collapsed to
-- UNIQUE (id), which is what role_pkey already says — and no foreign key uses
-- it as its referenced index, so it is an index kept warm for nobody.
--
-- SET LOCAL lock_timeout: each ALTER takes an ACCESS EXCLUSIVE lock on a table
-- every authenticated request reads, and an unbounded wait would queue behind
-- one open transaction for as long as it lives.
SET LOCAL lock_timeout = '3s';

ALTER TABLE role DROP CONSTRAINT uq_role_ws_id;

ALTER TABLE role DROP COLUMN workspace_id;
ALTER TABLE role_assignment DROP COLUMN workspace_id;
