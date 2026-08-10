-- Reverse of 0204: take the `finance` grant back out of every role document.
--
-- A role carrying a grant on an object the code no longer knows is not
-- harmless: Parse rejects a document naming an object outside the closed set,
-- so every role read would start failing. Removing the key is what makes the
-- down actually reversible.
--
-- The workspace binding is required for the same reason the up needs it: role
-- carries FORCE row-level security, and an UPDATE with no bound workspace is
-- filtered to zero rows and reports success.
DO $$
DECLARE ws uuid;
BEGIN
  FOR ws IN SELECT id FROM workspace LOOP
    PERFORM set_config('app.workspace_id', ws::text, true);

    UPDATE role SET permissions = permissions #- '{objects,finance}'
     WHERE permissions->'objects' ? 'finance'
       AND role.workspace_id = ws;
  END LOOP;
END $$;
