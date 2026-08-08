-- Down: the CIMD rows go before the constraint narrows again, or the ALTER
-- fails against exactly the data this migration created.
--
-- Per workspace, bound and predicated, because oauth_client carries FORCE
-- row-level security: unbound, the policy expression is NULL and the DELETE
-- removes zero rows while reporting success — leaving the ALTER below to fail
-- on rows nobody can see. The predicate is the second half of the same rule:
-- an executor RLS does not filter (a superuser, or the BYPASSRLS role every dev
-- machine runs as) sees every workspace on every iteration.
DO $$
DECLARE ws uuid;
BEGIN
  FOR ws IN SELECT id FROM workspace LOOP
    PERFORM set_config('app.workspace_id', ws::text, true);

    DELETE FROM oauth_client
     WHERE created_via = 'cimd'
       AND oauth_client.workspace_id = ws;
  END LOOP;
END $$;

ALTER TABLE oauth_client DROP COLUMN IF EXISTS metadata_expires_at;
ALTER TABLE oauth_client DROP CONSTRAINT oauth_client_created_via_check;
ALTER TABLE oauth_client ADD CONSTRAINT oauth_client_created_via_check
  CHECK (created_via IN ('dcr', 'admin'));
