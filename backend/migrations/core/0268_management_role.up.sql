-- 0268: the sixth system role, and two display names (ADR-0110 / A161).
--
-- Roles are seeded by APP code once per workspace (identity.seedSystemRoles)
-- and never re-synced, so an installation that booted before this release
-- has five system roles and no way to grow a sixth. This is the first
-- migration that INSERTS a role rather than amending one; every earlier RBAC
-- migration is a guarded UPDATE of a document that already exists.
--
-- What it does:
--   1. Inserts `management` — the manager object grid at row_scope `all`,
--      the sales leader who sees every team's pipeline and holds none of the
--      admin governance actions (those key on the literal `admin` role, so a
--      wide row scope here widens nothing they administer). Guarded on key
--      absence: `role_key_unique` is installation-wide since 0225, so an
--      installation with more than one workspace row receives one row, in the
--      first workspace, and a re-run inserts nothing.
--   2. Renames `manager` to "Team Lead" and `rep` to "Member" — but ONLY where
--      the name is still the shipped default ('Manager' / 'Rep'). The detection
--      rule is the old default name itself: an operator who renamed a role has a
--      name that no longer equals the default, and that name survives.
--
-- The management document below is derived from
-- migrations/testdata/rbac_seeded_defaults.json (itself pinned to
-- policy.MustDefaultJSON by identity's rbacfixture_test); a unit test in this
-- package holds the literal to that fixture so the two cannot drift.
DO $$
DECLARE ws uuid;
BEGIN
  -- The key was never reserved before this release. A row that already spells
  -- it and is NOT the system role would be picked by every by-key lookup
  -- (invite, role change) with whatever document it happens to carry, so the
  -- upgrade stops rather than adopting it silently.
  IF EXISTS (SELECT 1 FROM role WHERE key = 'management' AND NOT is_system) THEN
    RAISE EXCEPTION '0268: a non-system role already uses the key ''management''; rename it before upgrading';
  END IF;
  FOR ws IN SELECT id FROM workspace ORDER BY created_at, id LOOP
    INSERT INTO role (workspace_id, key, name, is_system, permissions)
    SELECT ws, 'management', 'Management', true,
      '{"objects":{"activity":{"create":true,"delete":true,"read":true,"update":true},"ai_model_rate":{"create":false,"delete":false,"read":false,"update":false},"automation":{"create":false,"delete":false,"read":true,"update":false},"capture_settings":{"create":true,"delete":false,"read":true,"update":false},"capture_trace":{"create":false,"delete":false,"read":true,"update":false},"channel_connection":{"create":false,"delete":false,"read":true,"update":false},"computed_field":{"create":false,"delete":false,"read":true,"update":false},"contract":{"create":true,"delete":true,"read":true,"update":true},"custom_field":{"create":false,"delete":false,"read":true,"update":false},"deal":{"create":true,"delete":true,"read":true,"update":true},"embedding_reindex":{"create":false,"delete":false,"read":false,"update":false},"finance":{"create":false,"delete":false,"read":true,"update":false},"fx_rate":{"create":false,"delete":false,"read":false,"update":false},"import_run":{"create":false,"delete":false,"read":false,"update":false},"installation_settings":{"create":false,"delete":false,"read":true,"update":false},"integrations":{"create":false,"delete":false,"read":true,"update":false},"lead":{"create":true,"delete":true,"read":true,"update":true},"license":{"create":false,"delete":false,"read":false,"update":false},"list":{"create":true,"delete":true,"read":true,"update":true},"offer":{"create":true,"delete":true,"read":true,"update":true},"offer_template":{"create":true,"delete":true,"read":true,"update":true},"organization":{"create":true,"delete":true,"read":true,"update":true},"overlay_connection":{"create":false,"delete":false,"read":true,"update":false},"partner":{"create":true,"delete":true,"read":true,"update":true},"person":{"create":true,"delete":true,"read":true,"update":true},"pipeline":{"create":false,"delete":false,"read":true,"update":false},"product":{"create":true,"delete":true,"read":true,"update":true},"project":{"create":true,"delete":true,"read":true,"update":true},"quota":{"create":false,"delete":false,"read":true,"update":false},"relationship":{"create":true,"delete":true,"read":true,"update":true},"retention_policy":{"create":false,"delete":false,"read":false,"update":false},"saved_view":{"create":true,"delete":true,"read":true,"update":true},"signal":{"create":true,"delete":true,"read":true,"update":true},"tag":{"create":true,"delete":true,"read":true,"update":true},"voice_profile":{"create":true,"delete":true,"read":true,"update":true},"webhook_subscription":{"create":false,"delete":false,"read":true,"update":false}},"row_scope":"all"}'::jsonb
    WHERE NOT EXISTS (SELECT 1 FROM role WHERE key = 'management');
  END LOOP;
END $$;

UPDATE role SET name = 'Team Lead' WHERE is_system AND key = 'manager' AND name = 'Manager';
UPDATE role SET name = 'Member'    WHERE is_system AND key = 'rep'     AND name = 'Rep';
