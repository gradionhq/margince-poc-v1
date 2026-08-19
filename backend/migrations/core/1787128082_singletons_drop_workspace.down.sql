-- Reverse of 1787128082: the singletons carry the tenant column again.
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
ALTER TABLE field_provenance ADD COLUMN workspace_id uuid;
ALTER TABLE idempotency_key ADD COLUMN workspace_id uuid;
ALTER TABLE linkedin_account ADD COLUMN workspace_id uuid;
ALTER TABLE linkedin_connection ADD COLUMN workspace_id uuid;
ALTER TABLE signal_thread_scan ADD COLUMN workspace_id uuid;
ALTER TABLE suggestion_dismissal ADD COLUMN workspace_id uuid;
ALTER TABLE user_record_view ADD COLUMN workspace_id uuid;

DO $$
DECLARE
  live uuid := (SELECT id FROM workspace WHERE archived_at IS NULL ORDER BY created_at LIMIT 1);
  t    text;
BEGIN
  FOREACH t IN ARRAY ARRAY[
    'field_provenance', 'idempotency_key', 'linkedin_account',
    'linkedin_connection', 'signal_thread_scan', 'suggestion_dismissal',
    'user_record_view'
  ] LOOP
    EXECUTE format('UPDATE %I SET workspace_id = $1 WHERE workspace_id IS NULL', t) USING live;
    EXECUTE format('ALTER TABLE %I ALTER COLUMN workspace_id SET NOT NULL', t);
  END LOOP;
END $$;

ALTER TABLE field_provenance ADD CONSTRAINT field_provenance_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE idempotency_key ADD CONSTRAINT idempotency_key_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE linkedin_account ADD CONSTRAINT linkedin_account_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE linkedin_connection ADD CONSTRAINT linkedin_connection_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE signal_thread_scan ADD CONSTRAINT signal_thread_scan_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE CASCADE;
ALTER TABLE suggestion_dismissal ADD CONSTRAINT suggestion_dismissal_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE user_record_view ADD CONSTRAINT user_record_view_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;

DROP INDEX idx_field_provenance_object;
DROP INDEX idx_linkedin_connection_email;
DROP INDEX idx_linkedin_connection_match;
DROP INDEX idx_linkedin_connection_org;
DROP INDEX suggestion_dismissal_organization_ix;

CREATE INDEX idx_field_provenance_object ON field_provenance
  (workspace_id, object_type, object_id, field_name, captured_at DESC);
CREATE INDEX idx_linkedin_connection_email ON linkedin_connection (workspace_id, lower(email))
  WHERE email IS NOT NULL AND tombstoned_at IS NULL;
CREATE INDEX idx_linkedin_connection_match ON linkedin_connection
  (workspace_id, normalized_name, normalized_company) WHERE tombstoned_at IS NULL;
CREATE INDEX idx_linkedin_connection_org ON linkedin_connection (workspace_id, matched_org_id)
  WHERE matched_org_id IS NOT NULL AND tombstoned_at IS NULL;
CREATE INDEX suggestion_dismissal_organization_ix ON suggestion_dismissal (workspace_id, organization_id);
