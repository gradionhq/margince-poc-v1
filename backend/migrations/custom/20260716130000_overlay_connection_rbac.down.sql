-- AMENDED (ADR-0091 §8 phase D): the `AND role.workspace_id = ws` predicate on
-- each statement below was removed, and with it the per-workspace loop's reason
-- to exist. `dbmigrate.Up` applies ALL of `core` before ANY of `custom`, so on
-- a FRESH database this file runs against the FINAL core schema — where
-- `role.workspace_id` no longer exists — and the install fails outright.
-- Amending is what keeps a fresh install working; a deployed database already
-- ran the original and is unaffected, because a single-organization
-- installation (ADR-0061) has exactly one workspace, which made the predicate a
-- no-op the day it was written.
DO $$
BEGIN
    UPDATE role SET permissions = permissions #- '{objects,overlay_connection}'
    WHERE (is_system);
END $$;
