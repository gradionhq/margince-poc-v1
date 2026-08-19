-- 1787123080: the authorization satellites drop the tenant column
-- (ADR-0091 §8 phase D).
--
-- Five tables that say who someone is and what they may reach:
--
--   team, team_membership      the row-scope grouping and its members
--   record_grant               a record shared with one subject
--   extension_secret           an extension's per-user or per-install secret
--   onboarding_wizard_state    how far one human got through setup
--
-- Every uniqueness these tables rely on is already tenant-free — phase B did
-- that — so nothing here changes what may collide. team.name is unique
-- installation-wide, a record grant is unique by (record, subject), and an
-- extension secret by (extension, user, key) or (extension, key) for the
-- install-wide one.
--
-- uq_team_ws_id goes with the column: phase B collapsed it to UNIQUE (id), a
-- second copy of the table's own primary key, kept alive only as a composite FK
-- target that phase C rewrote away. Verified referenced by no foreign key
-- before dropping.
--
-- role and role_assignment are NOT here, and not because they are hard to
-- change. THREE SHIPPED MIGRATIONS name role.workspace_id — core/0192,
-- custom/20260806120000 and custom/20260716130000 — and the RBAC replay suites
-- re-apply them against a head schema to prove what they wrote. Dropping the
-- column makes those historical statements unrunnable, so the column and the
-- suites have to move together, in their own change.
--
-- app_user and session are NOT here either. They are what MustWorkspace
-- ultimately resolves through, and they go last in this group.

-- lock_timeout for the reason core/0139 sets it: every ALTER TABLE below needs
-- ACCESS EXCLUSIVE on a live table, and a pending strong request queues behind
-- whatever transaction is already running while every write arriving after it
-- queues behind the request. Unbounded, one idle-in-transaction session turns
-- this migration into an installation-wide write stall on `team`,
-- `team_membership` and `record_grant`; three seconds makes it a fast, loud
-- failure the deploy retries.
--
-- Added by the change that armed the lock-timeout fitness function, which
-- reported this file as its first real catch. Safe on an already-applied
-- database for the same reason it is on 1787111736: lock_timeout governs how a
-- lock is ACQUIRED and touches neither schema nor data.
SET LOCAL lock_timeout = '3s';

ALTER TABLE onboarding_wizard_state DROP CONSTRAINT onboarding_wizard_state_workspace_id_fkey;
ALTER TABLE onboarding_wizard_state DROP COLUMN workspace_id;

ALTER TABLE extension_secret DROP CONSTRAINT extension_secret_workspace_id_fkey;
ALTER TABLE extension_secret DROP COLUMN workspace_id;

ALTER TABLE record_grant DROP CONSTRAINT record_grant_workspace_id_fkey;
ALTER TABLE record_grant DROP COLUMN workspace_id;

ALTER TABLE team_membership DROP CONSTRAINT team_membership_workspace_id_fkey;
ALTER TABLE team_membership DROP COLUMN workspace_id;

ALTER TABLE team DROP CONSTRAINT uq_team_ws_id;
ALTER TABLE team DROP CONSTRAINT team_workspace_id_fkey;
ALTER TABLE team DROP COLUMN workspace_id;

-- The three indexes that led with the column, recreated on what actually
-- selects rows: the record a grant is about, the subject it is for, and the
-- user an extension secret belongs to.
CREATE INDEX idx_record_grant_record ON record_grant (record_type, record_id);
CREATE INDEX idx_record_grant_subject ON record_grant (subject_type, subject_id);
CREATE INDEX extension_secret_workspace_user ON extension_secret (user_id);
