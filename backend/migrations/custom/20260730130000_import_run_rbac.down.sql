-- AMENDED (ADR-0091 §8 phase D): the `AND role.workspace_id = ws` predicate on
-- each statement below was removed, and with it the per-workspace loop's reason
-- to exist. `dbmigrate.Up` applies ALL of `core` before ANY of `custom`, so on
-- a FRESH database this file runs against the FINAL core schema — where
-- `role.workspace_id` no longer exists — and the install fails outright.
-- Amending is what keeps a fresh install working; a deployed database already
-- ran the original and is unaffected, because a single-organization
-- installation (ADR-0061) has exactly one workspace, which made the predicate a
-- no-op the day it was written.
-- Removes the object from the two roles the up grants it to, and only those.
-- Stripping every system role would erase the key a freshly seeded installation
-- writes for manager/rep/read_only — the up never touched those, so a rollback
-- has no business removing them.
DO $$
BEGIN
    UPDATE role SET permissions = permissions #- '{objects,import_run}'
    WHERE (is_system AND key IN ('admin','ops')
      AND permissions->'objects' ? 'import_run');
END $$;
