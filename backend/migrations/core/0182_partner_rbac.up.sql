-- Backfill the `partner` RBAC object into the seeded system-role policy
-- documents of EXISTING workspaces. It entered the code-side seed
-- (identity/internal/policy) without a backfill, so a workspace bootstrapped
-- before that release holds no grant and 403s on every partner route.
--
-- Posture: admin/ops/manager work partners fully; a rep READS them but does
-- not create or edit — a partner is a relationship the business owns, not a
-- record an individual rep works, which is why the rep tier here is narrower
-- than for person/organization/deal.
DO $$
DECLARE ws uuid;
BEGIN
  FOR ws IN SELECT id FROM workspace LOOP
    PERFORM set_config('app.workspace_id', ws::text, true);
    UPDATE role SET permissions = jsonb_set(
      permissions, '{objects,partner}',
      '{"create":true,"read":true,"update":true,"delete":true}'::jsonb)
    WHERE (is_system AND key IN ('admin','ops','manager')
      AND NOT permissions->'objects' ? 'partner')
      AND role.workspace_id = ws;

    UPDATE role SET permissions = jsonb_set(
      permissions, '{objects,partner}',
      '{"create":false,"read":true,"update":false,"delete":false}'::jsonb)
    WHERE (is_system AND key IN ('rep','read_only')
      AND NOT permissions->'objects' ? 'partner')
      AND role.workspace_id = ws;
  END LOOP;
END $$;
