-- Backfill the `ai_routing` RBAC object into EXISTING workspaces' seeded role
-- documents. New workspaces get it from the code-side seed
-- (identity/internal/policy). Shipping a new object without this backfill is how
-- one "works on a fresh database and 403s everywhere else".
--
-- This object governs the tier→model binding (ai-operational-spec §1.4): which
-- vendor an installation's text is sent to. It is deliberately its own object
-- rather than part of installation_settings, because whoever may rename the
-- organization has no business re-pointing where its people's correspondence is
-- processed, and those become one grant the moment the two share an object.
--
-- NARROW on both verbs, and read is narrow too. Seeing which models are bound
-- does not need this grant — the AI profile surface answers that from the
-- running config rather than from a settings read — so a broad read would buy a
-- rep nothing while widening the reach of the object that governs egress. There
-- is no create or delete: a setting is read and updated, and an absent row is
-- its registered default rather than a missing record.
--
-- Every role is named, including the ones that get nothing. An explicit
-- all-false document and an absent key both deny today, but only the first says
-- the denial was decided — and it is what keeps this backfill's end state equal
-- to the matrix the server seeds, which migrations/rbacreplay is the proof of.
--
-- The `role` table still carries `workspace_id`, so this backfill keeps the
-- workspace loop and the per-statement predicate the tenant-scope rule requires.
DO $$
DECLARE ws uuid;
BEGIN
  FOR ws IN SELECT id FROM workspace LOOP
    PERFORM set_config('app.workspace_id', ws::text, true);

    UPDATE role SET permissions = jsonb_set(
      permissions, '{objects,ai_routing}',
      '{"create":false,"read":true,"update":true,"delete":false}'::jsonb)
    WHERE (is_system AND key IN ('admin', 'ops')
      AND NOT permissions->'objects' ? 'ai_routing')
      AND role.workspace_id = ws;

    UPDATE role SET permissions = jsonb_set(
      permissions, '{objects,ai_routing}',
      '{"create":false,"read":false,"update":false,"delete":false}'::jsonb)
    WHERE (is_system AND key IN ('management', 'manager', 'rep', 'read_only')
      AND NOT permissions->'objects' ? 'ai_routing')
      AND role.workspace_id = ws;
  END LOOP;
END $$;
