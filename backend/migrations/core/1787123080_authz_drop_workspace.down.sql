-- Reverse of 1787123080: the authorization satellites carry the tenant column
-- again.
--
-- The backfill reads the LIVE workspace — archived_at IS NULL, oldest first.
-- 0217 refuses more than one live tenant and 0272 refuses to proceed while an
-- archived one still holds records, so there was exactly one workspace these
-- rows could have belonged to when the forward half ran. Ordering by created_at
-- alone would hand every restored row to whichever workspace was created first,
-- archived or not.
--
-- If no live workspace exists and a table is not empty, SET NOT NULL fails and
-- the rollback stops — the honest outcome, since no value this migration could
-- write would be true. A rollback on an empty database (the reverse-and-reapply
-- lane) has nothing to attribute and passes.

-- lock_timeout, matching the up half: re-adding these columns needs the same
-- ACCESS EXCLUSIVE, and a rollback runs at exactly the moment nobody can afford
-- it to hang.
SET LOCAL lock_timeout = '3s';

ALTER TABLE team ADD COLUMN workspace_id uuid;
ALTER TABLE team_membership ADD COLUMN workspace_id uuid;
ALTER TABLE record_grant ADD COLUMN workspace_id uuid;
ALTER TABLE extension_secret ADD COLUMN workspace_id uuid;
ALTER TABLE onboarding_wizard_state ADD COLUMN workspace_id uuid;

DO $$
DECLARE
  live uuid := (SELECT id FROM workspace WHERE archived_at IS NULL ORDER BY created_at LIMIT 1);
  t    text;
BEGIN
  FOREACH t IN ARRAY ARRAY[
    'team', 'team_membership',
    'record_grant', 'extension_secret', 'onboarding_wizard_state'
  ] LOOP
    EXECUTE format('UPDATE %I SET workspace_id = $1 WHERE workspace_id IS NULL', t) USING live;
    EXECUTE format('ALTER TABLE %I ALTER COLUMN workspace_id SET NOT NULL', t);
  END LOOP;
END $$;

ALTER TABLE team ADD CONSTRAINT team_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE team_membership ADD CONSTRAINT team_membership_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE record_grant ADD CONSTRAINT record_grant_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE extension_secret ADD CONSTRAINT extension_secret_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE CASCADE;
ALTER TABLE onboarding_wizard_state ADD CONSTRAINT onboarding_wizard_state_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;

-- UNIQUE (id), which is what the migration before this one left: phase B
-- collapsed it from its composite form and this restores that state.
ALTER TABLE team ADD CONSTRAINT uq_team_ws_id UNIQUE (id);

DROP INDEX idx_record_grant_record;
DROP INDEX idx_record_grant_subject;
DROP INDEX extension_secret_workspace_user;

CREATE INDEX idx_record_grant_record ON record_grant (workspace_id, record_type, record_id);
CREATE INDEX idx_record_grant_subject ON record_grant (workspace_id, subject_type, subject_id);
CREATE INDEX extension_secret_workspace_user ON extension_secret (workspace_id, user_id);
