-- Down: the CIMD rows go before the constraint narrows again, or the ALTER
-- fails against exactly the data this migration created.
--
-- AND SO DOES EVERYTHING THAT POINTS AT THEM, in dependency order. A CIMD
-- client that anyone actually connected through has an oauth_grant with
-- ON DELETE RESTRICT onto it, and passports under that grant with a RESTRICT
-- of their own — so a rollback of a used feature would fail on its own data,
-- which is the one thing a down migration must not do.
--
-- The data-loss policy, stated rather than implied: rolling this back REVOKES
-- every connection made through a metadata document. There is no way to keep
-- them — the column that says what such a client IS goes with the constraint —
-- and a connection whose client row is gone is authority nobody can see or
-- revoke. Revoking it here is the honest end.
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

    -- Passports first: they RESTRICT the grant, which RESTRICTs the client.
    DELETE FROM passport
     WHERE passport.workspace_id = ws
       AND oauth_grant_id IN (
         SELECT g.id FROM oauth_grant g
          JOIN oauth_client c ON c.workspace_id = g.workspace_id AND c.client_id = g.client_id
         WHERE g.workspace_id = ws AND c.created_via = 'cimd');

    DELETE FROM oauth_grant
     WHERE oauth_grant.workspace_id = ws
       AND client_id IN (
         SELECT client_id FROM oauth_client
          WHERE oauth_client.workspace_id = ws AND created_via = 'cimd');

    DELETE FROM oauth_authorization_code
     WHERE oauth_authorization_code.workspace_id = ws
       AND client_id IN (
         SELECT client_id FROM oauth_client
          WHERE oauth_client.workspace_id = ws AND created_via = 'cimd');

    DELETE FROM oauth_client
     WHERE created_via = 'cimd'
       AND oauth_client.workspace_id = ws;
  END LOOP;
END $$;

ALTER TABLE oauth_client DROP COLUMN IF EXISTS metadata_expires_at;
ALTER TABLE oauth_client DROP CONSTRAINT oauth_client_created_via_check;
ALTER TABLE oauth_client ADD CONSTRAINT oauth_client_created_via_check
  CHECK (created_via IN ('dcr', 'admin'));
