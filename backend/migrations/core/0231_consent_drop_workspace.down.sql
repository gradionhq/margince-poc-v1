-- Reverse of 0231: the eight tables carry the tenant column again.
--
-- The backfill reads `workspace` because there is exactly one to read: 0217's
-- pre-flight refuses to run against a database holding more than one live
-- workspace, and ADR-0061 §3 has the API refusing to start in that state. If
-- `workspace` is empty and a table is not, SET NOT NULL fails and the rollback
-- stops — the honest outcome, since no value this migration could write would
-- be true.

ALTER TABLE consent_purpose ADD COLUMN workspace_id uuid;
ALTER TABLE person_consent ADD COLUMN workspace_id uuid;
ALTER TABLE consent_event ADD COLUMN workspace_id uuid;
ALTER TABLE consent_doi_token ADD COLUMN workspace_id uuid;
ALTER TABLE consent_qualifying_event ADD COLUMN workspace_id uuid;
ALTER TABLE consent_existing_customer_flag ADD COLUMN workspace_id uuid;
ALTER TABLE data_subject_request ADD COLUMN workspace_id uuid;
ALTER TABLE preference_token ADD COLUMN workspace_id uuid;

DO $$
DECLARE ws uuid := (SELECT id FROM workspace ORDER BY created_at LIMIT 1);
BEGIN
  UPDATE consent_purpose SET workspace_id = ws;
  UPDATE person_consent SET workspace_id = ws;
  UPDATE consent_event SET workspace_id = ws;
  UPDATE consent_doi_token SET workspace_id = ws;
  UPDATE consent_qualifying_event SET workspace_id = ws;
  UPDATE consent_existing_customer_flag SET workspace_id = ws;
  UPDATE data_subject_request SET workspace_id = ws;
  UPDATE preference_token SET workspace_id = ws;
END $$;

ALTER TABLE consent_purpose ALTER COLUMN workspace_id SET NOT NULL;
ALTER TABLE person_consent ALTER COLUMN workspace_id SET NOT NULL;
ALTER TABLE consent_event ALTER COLUMN workspace_id SET NOT NULL;
ALTER TABLE consent_doi_token ALTER COLUMN workspace_id SET NOT NULL;
ALTER TABLE consent_qualifying_event ALTER COLUMN workspace_id SET NOT NULL;
ALTER TABLE consent_existing_customer_flag ALTER COLUMN workspace_id SET NOT NULL;
ALTER TABLE data_subject_request ALTER COLUMN workspace_id SET NOT NULL;
ALTER TABLE preference_token ALTER COLUMN workspace_id SET NOT NULL;

ALTER TABLE consent_purpose ADD CONSTRAINT consent_purpose_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE person_consent ADD CONSTRAINT person_consent_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE consent_event ADD CONSTRAINT consent_event_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE consent_doi_token ADD CONSTRAINT consent_doi_token_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE consent_qualifying_event ADD CONSTRAINT consent_qualifying_event_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE consent_existing_customer_flag ADD CONSTRAINT consent_existing_customer_flag_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE data_subject_request ADD CONSTRAINT data_subject_request_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE preference_token ADD CONSTRAINT preference_token_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;

ALTER TABLE consent_purpose ADD CONSTRAINT uq_consent_purpose_ws_id UNIQUE (id);

DROP INDEX consent_qualifying_event_person_ix;
CREATE INDEX consent_qualifying_event_person_ix ON consent_qualifying_event (workspace_id, person_id, occurred_at DESC);

DROP INDEX idx_consent_doi_token_person;
CREATE INDEX idx_consent_doi_token_person ON consent_doi_token (workspace_id, person_id, purpose_id);

DROP INDEX idx_consent_event_lead;
CREATE INDEX idx_consent_event_lead ON consent_event (workspace_id, lead_id, captured_at DESC) WHERE lead_id IS NOT NULL;

DROP INDEX idx_consent_event_person;
CREATE INDEX idx_consent_event_person ON consent_event (workspace_id, person_id, captured_at DESC);

DROP INDEX idx_dsr_open;
CREATE INDEX idx_dsr_open ON data_subject_request (workspace_id, due_at) WHERE status IN ('open', 'in_progress');
