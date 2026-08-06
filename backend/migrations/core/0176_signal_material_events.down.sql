-- Reverse of 0176. The kinds go back to the original six, so any row a
-- producer wrote under a new kind must go first: leaving it would fail the
-- restored CHECK and strand the migration half-applied.

DROP TABLE IF EXISTS signal_thread_scan;

DROP INDEX IF EXISTS uq_signal_fingerprint;
ALTER TABLE signal DROP COLUMN IF EXISTS fingerprint;

-- The migration role is NOSUPERUSER NOBYPASSRLS (scripts/deploy/db-bootstrap.sql),
-- so FORCE RLS binds it like any other role and this DELETE would match zero
-- rows with no GUC set — leaving the new-kind rows in place and failing the
-- restored six-kind CHECK below. The cleanup crosses every workspace by
-- design, so it runs with the policy lifted and puts it back immediately.
ALTER TABLE signal NO FORCE ROW LEVEL SECURITY;
ALTER TABLE signal DISABLE ROW LEVEL SECURITY;

DO $$
DECLARE ws uuid;
BEGIN
  FOR ws IN SELECT id FROM workspace LOOP
    PERFORM set_config('app.workspace_id', ws::text, true);
    DELETE FROM signal WHERE kind IN
      ('contract_ended','new_opportunity','commitment_made','ghosted_thread');
  END LOOP;
END $$;


ALTER TABLE signal ENABLE ROW LEVEL SECURITY;
ALTER TABLE signal FORCE ROW LEVEL SECURITY;

ALTER TABLE signal DROP CONSTRAINT signal_kind_check;
ALTER TABLE signal ADD CONSTRAINT signal_kind_check CHECK (kind IN (
  'stalled_deal','champion_left','reengagement','buying_intent','risk','other'));