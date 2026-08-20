-- AMENDED (ADR-0091 §8 phase D): the `AND role.workspace_id = ws` predicate on
-- each statement below was removed, and with it the per-workspace loop's reason
-- to exist. `dbmigrate.Up` applies ALL of `core` before ANY of `custom`, so on
-- a FRESH database this file runs against the FINAL core schema — where
-- `role.workspace_id` no longer exists — and the install fails outright.
-- Amending is what keeps a fresh install working; a deployed database already
-- ran the original and is unaffected, because a single-organization
-- installation (ADR-0061) has exactly one workspace, which made the predicate a
-- no-op the day it was written.
-- 20260730130000: backfill the `import_run` RBAC object into the seeded
-- system-role policy documents of EXISTING workspaces (new workspaces get
-- it from the code-side seed, identity/internal/policy). Fork-owned
-- migration (ADR-0017 custom namespace) — import_run is the migration
-- module's own object.
--
-- Posture mirrors the overlay_connection backfill
-- (20260716130000_overlay_connection_rbac.up.sql): a migration run is a
-- workspace-wide bulk mutation of the estate (the flip's importer runs
-- through it), so every verb is admin/ops-only; other roles hold no
-- grant at all — a rep neither starts nor reads migration runs.
DO $$
BEGIN
    UPDATE role SET permissions = jsonb_set(
      permissions, '{objects,import_run}',
      '{"create":true,"read":true,"update":true,"delete":true}'::jsonb)
    WHERE (is_system AND key IN ('admin','ops')
      AND NOT permissions->'objects' ? 'import_run');
END $$;
