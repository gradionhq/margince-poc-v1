-- 0056: lead-scoped consent (E12.20, data-model §7). A public form or
-- LinkedIn capture obtains consent from someone who is still a lead —
-- there is no person row yet to hang it on. Consent state and proof gain
-- a lead arm; on promotion the state re-points to the person (proof rows
-- stay as written — the historical lead-scoped events ARE the proof).
ALTER TABLE person_consent ALTER COLUMN person_id DROP NOT NULL;
ALTER TABLE person_consent
  ADD COLUMN lead_id uuid NULL REFERENCES lead(id) ON DELETE CASCADE;
ALTER TABLE person_consent ADD CONSTRAINT person_consent_subject
  CHECK (person_id IS NOT NULL OR lead_id IS NOT NULL);
ALTER TABLE person_consent ADD CONSTRAINT person_consent_lead_unique
  UNIQUE (workspace_id, lead_id, purpose_id);

ALTER TABLE consent_event ALTER COLUMN person_id DROP NOT NULL;
ALTER TABLE consent_event
  ADD COLUMN lead_id uuid NULL REFERENCES lead(id);
ALTER TABLE consent_event ADD CONSTRAINT consent_event_subject
  CHECK (person_id IS NOT NULL OR lead_id IS NOT NULL);
CREATE INDEX idx_consent_event_lead ON consent_event (workspace_id, lead_id, captured_at DESC)
  WHERE lead_id IS NOT NULL;
