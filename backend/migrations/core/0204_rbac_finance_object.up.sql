-- 0204: give every existing role a grant on the `finance` object.
--
-- The role seed writes each system role's permission document ONCE, at
-- workspace creation, and never re-syncs it. So a new RBAC object works on a
-- fresh database and 403s everywhere else, permanently — the installation that
-- has been running for a month is exactly the one that would never see the
-- finance panel. This is the second half of adding the object, not a
-- convenience.
--
-- The grants match the registered defaults in identity's policy.go, and the
-- reasoning is overlay_connection's: READ is broad, because every role that
-- opens a company page should see whether the customer pays on time, and
-- connecting or disconnecting the accounting source is destructive
-- workspace-wide config that belongs to admin and ops.
--
-- No role gets create/update/delete on a finance RECORD, because there is no
-- such action to grant: the mirror's read-only posture is the absence of the
-- grant (FIN-DDL-N-1), and these three verbs govern the CONNECTION.
--
-- Only a document that does not already carry the key is touched, so an
-- operator who has already tuned the role keeps their answer.
DO $$
DECLARE ws uuid;
BEGIN
  FOR ws IN SELECT id FROM workspace LOOP
    PERFORM set_config('app.workspace_id', ws::text, true);

    UPDATE role SET permissions = jsonb_set(
      permissions, '{objects,finance}',
      '{"create":true,"read":true,"update":true,"delete":true}'::jsonb)
    WHERE (is_system AND key IN ('admin','ops')
      AND NOT permissions->'objects' ? 'finance')
      AND role.workspace_id = ws;

    UPDATE role SET permissions = jsonb_set(
      permissions, '{objects,finance}',
      '{"create":false,"read":true,"update":false,"delete":false}'::jsonb)
    WHERE (is_system AND key IN ('manager','rep','read_only')
      AND NOT permissions->'objects' ? 'finance')
      AND role.workspace_id = ws;
  END LOOP;
END $$;
