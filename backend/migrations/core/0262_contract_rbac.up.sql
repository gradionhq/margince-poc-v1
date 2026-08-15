-- 0262: backfill the `contract` RBAC object into EXISTING workspaces' seeded
-- role documents. New workspaces get it from the code-side seed
-- (identity/internal/policy). Shipping a new object without this backfill is how
-- one "works on a fresh database and 403s everywhere else".
--
-- READ is broad: a contract is what makes an account's commercial state legible,
-- and every role that opens a company page needs to see whether the customer is
-- under contract and when it ends.
--
-- WRITE follows the commercial-record posture the deal and offer objects already
-- set. admin, ops and manager hold the full verb set. A rep creates and maintains
-- the agreements they close but does not archive one — archiving is where the
-- evidence behind a won deal disappears from the surfaces that count it, so it
-- stays with manager/admin like every other record here. read_only reads.
--
-- The `role` table still carries `workspace_id`, so this backfill keeps the
-- workspace loop and the per-statement predicate the tenant-scope rule requires.
-- The `contract` table itself carries no tenant column (ADR-0091/A136) — the two
-- facts sit side by side here only because the tenancy retirement is mid-flight.
DO $$
DECLARE ws uuid;
BEGIN
  FOR ws IN SELECT id FROM workspace LOOP
    PERFORM set_config('app.workspace_id', ws::text, true);

    UPDATE role SET permissions = jsonb_set(
      permissions, '{objects,contract}',
      '{"create":true,"read":true,"update":true,"delete":true}'::jsonb)
    WHERE (is_system AND key IN ('admin', 'ops', 'manager')
      AND NOT permissions->'objects' ? 'contract')
      AND role.workspace_id = ws;

    UPDATE role SET permissions = jsonb_set(
      permissions, '{objects,contract}',
      '{"create":true,"read":true,"update":true,"delete":false}'::jsonb)
    WHERE (is_system AND key = 'rep'
      AND NOT permissions->'objects' ? 'contract')
      AND role.workspace_id = ws;

    UPDATE role SET permissions = jsonb_set(
      permissions, '{objects,contract}',
      '{"create":false,"read":true,"update":false,"delete":false}'::jsonb)
    WHERE (is_system AND key = 'read_only'
      AND NOT permissions->'objects' ? 'contract')
      AND role.workspace_id = ws;
  END LOOP;
END $$;
