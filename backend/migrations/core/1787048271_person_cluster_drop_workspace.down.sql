-- Reverse of 1787048271: the person cluster carries the tenant column again.
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
ALTER TABLE person ADD COLUMN workspace_id uuid;
ALTER TABLE person_email ADD COLUMN workspace_id uuid;
ALTER TABLE person_phone ADD COLUMN workspace_id uuid;
ALTER TABLE person_social ADD COLUMN workspace_id uuid;
ALTER TABLE person_profile_field ADD COLUMN workspace_id uuid;
ALTER TABLE person_channel_identity ADD COLUMN workspace_id uuid;
ALTER TABLE person_signature_enrich_state ADD COLUMN workspace_id uuid;
ALTER TABLE person_moment_dismissal ADD COLUMN workspace_id uuid;

DO $$
DECLARE
  live uuid := (SELECT id FROM workspace WHERE archived_at IS NULL ORDER BY created_at LIMIT 1);
  t    text;
BEGIN
  FOREACH t IN ARRAY ARRAY[
    'person', 'person_email', 'person_phone', 'person_social',
    'person_profile_field', 'person_channel_identity',
    'person_signature_enrich_state', 'person_moment_dismissal'
  ] LOOP
    EXECUTE format('UPDATE %I SET workspace_id = $1 WHERE workspace_id IS NULL', t) USING live;
    EXECUTE format('ALTER TABLE %I ALTER COLUMN workspace_id SET NOT NULL', t);
  END LOOP;
END $$;

ALTER TABLE person ADD CONSTRAINT person_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE person_email ADD CONSTRAINT person_email_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE person_phone ADD CONSTRAINT person_phone_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE person_social ADD CONSTRAINT person_social_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE person_profile_field ADD CONSTRAINT person_profile_field_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE person_channel_identity ADD CONSTRAINT person_channel_identity_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE person_signature_enrich_state ADD CONSTRAINT person_signature_enrich_state_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE person_moment_dismissal ADD CONSTRAINT person_moment_dismissal_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;

-- UNIQUE (id), which is what the migration before this one left: phase B
-- collapsed it from its composite form and this restores that state.
ALTER TABLE person ADD CONSTRAINT uq_person_ws_id UNIQUE (id);

DROP INDEX idx_person_owner;
DROP INDEX idx_person_profile_field;
DROP INDEX idx_person_channel_identity_person;
DROP INDEX person_moment_dismissal_person_ix;

CREATE INDEX idx_person_owner ON person (workspace_id, owner_id) WHERE archived_at IS NULL;
CREATE INDEX idx_person_ws_live ON person (workspace_id) WHERE archived_at IS NULL;
CREATE INDEX idx_person_profile_field ON person_profile_field (workspace_id, person_id);
CREATE INDEX idx_person_channel_identity_person ON person_channel_identity (workspace_id, person_id);
CREATE INDEX person_moment_dismissal_person_ix ON person_moment_dismissal (workspace_id, person_id);
