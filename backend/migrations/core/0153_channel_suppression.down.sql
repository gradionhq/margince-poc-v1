-- 0147 rollback: the narrowed CHECK cannot admit the rows already written, so
-- the channel-identity suppressions are dropped first. Reversing this migration
-- therefore forgets which channel identities were erased — a re-applied 0147
-- starts from an empty channel suppression list.
DO $$
DECLARE ws uuid;
BEGIN
  FOR ws IN SELECT id FROM workspace LOOP
    PERFORM set_config('app.workspace_id', ws::text, true);
    DELETE FROM erasure_suppression
    WHERE (kind = 'channel_identity')
      AND erasure_suppression.workspace_id = ws;
  END LOOP;
END $$;


ALTER TABLE erasure_suppression DROP CONSTRAINT erasure_suppression_kind_check;
ALTER TABLE erasure_suppression
  ADD CONSTRAINT erasure_suppression_kind_check CHECK (kind IN ('email'));
