-- 0218: give every existing role a grant on the `integrations` object.
--
-- The role seed writes each system role's permission document ONCE, at
-- workspace creation, and never re-syncs it. So a new RBAC object works on a
-- fresh database and 403s everywhere else, permanently — the installation that
-- has been running for a month is exactly the one whose admin would never see
-- the Integrations panel. This is the second half of adding the object, not a
-- convenience.
--
-- The grants match the registered defaults in identity's policy.go, and the
-- split follows finance's: READ is broad, because a rep looking at a person
-- record should be able to see whether a provider is connected and why a value
-- is dated, and every one of the four states the card renders is a fact about
-- the installation rather than about a customer. Connecting a provider spends
-- the customer's money and sends their contacts' identifiers to a third party,
-- so create/update/delete belong to admin and ops alone.
--
-- Only a document that does not already carry the key is touched, so an
-- operator who has already tuned the role keeps their answer.
DO $$
DECLARE ws uuid;
BEGIN
  FOR ws IN SELECT id FROM workspace LOOP
    PERFORM set_config('app.workspace_id', ws::text, true);

    UPDATE role SET permissions = jsonb_set(
      permissions, '{objects,integrations}',
      '{"create":true,"read":true,"update":true,"delete":true}'::jsonb)
    WHERE (is_system AND key IN ('admin','ops')
      AND NOT permissions->'objects' ? 'integrations')
      AND role.workspace_id = ws;

    UPDATE role SET permissions = jsonb_set(
      permissions, '{objects,integrations}',
      '{"create":false,"read":true,"update":false,"delete":false}'::jsonb)
    WHERE (is_system AND key IN ('manager','rep','read_only')
      AND NOT permissions->'objects' ? 'integrations')
      AND role.workspace_id = ws;
  END LOOP;
END $$;
