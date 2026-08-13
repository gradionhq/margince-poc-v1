-- 0231: the consent module drops the tenant column (ADR-0091 §8 phase D).
--
-- Eight tables, five indexes, one redundant unique.
--
-- `preference_token` is the one to read twice. It was deliberately OUTSIDE
-- row-level security (0048) because it IS the resolver: the public preference
-- centre has no session, so the token was what told the server which tenant
-- the request belonged to. That question has one answer now — the identity
-- middleware binds the installation's workspace into every request context,
-- public paths included, before the preference middleware runs — so the
-- column was already answering a question nobody was asking.

DROP INDEX consent_qualifying_event_person_ix;
CREATE INDEX consent_qualifying_event_person_ix ON consent_qualifying_event (person_id, occurred_at DESC);

DROP INDEX idx_consent_doi_token_person;
CREATE INDEX idx_consent_doi_token_person ON consent_doi_token (person_id, purpose_id);

DROP INDEX idx_consent_event_lead;
CREATE INDEX idx_consent_event_lead ON consent_event (lead_id, captured_at DESC) WHERE lead_id IS NOT NULL;

DROP INDEX idx_consent_event_person;
CREATE INDEX idx_consent_event_person ON consent_event (person_id, captured_at DESC);

DROP INDEX idx_dsr_open;
CREATE INDEX idx_dsr_open ON data_subject_request (due_at) WHERE status IN ('open', 'in_progress');

ALTER TABLE consent_purpose DROP CONSTRAINT uq_consent_purpose_ws_id;

ALTER TABLE consent_purpose DROP COLUMN workspace_id;
ALTER TABLE person_consent DROP COLUMN workspace_id;
ALTER TABLE consent_event DROP COLUMN workspace_id;
ALTER TABLE consent_doi_token DROP COLUMN workspace_id;
ALTER TABLE consent_qualifying_event DROP COLUMN workspace_id;
ALTER TABLE consent_existing_customer_flag DROP COLUMN workspace_id;
ALTER TABLE data_subject_request DROP COLUMN workspace_id;
ALTER TABLE preference_token DROP COLUMN workspace_id;
