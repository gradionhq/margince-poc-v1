-- Lead-scoped rows cannot survive a person_id NOT NULL world; deleting
-- consent proof is a data loss this down-migration accepts explicitly
-- (dev/test rollback only — core migrations never roll back in prod).
DO $$
DECLARE ws uuid;
BEGIN
  FOR ws IN SELECT id FROM workspace LOOP
    PERFORM set_config('app.workspace_id', ws::text, true);
    DELETE FROM consent_event WHERE person_id IS NULL;
  END LOOP;
END $$;

DROP INDEX idx_consent_event_lead;
ALTER TABLE consent_event DROP CONSTRAINT consent_event_subject;
ALTER TABLE consent_event DROP COLUMN lead_id;
ALTER TABLE consent_event ALTER COLUMN person_id SET NOT NULL;

DO $$
DECLARE ws uuid;
BEGIN
  FOR ws IN SELECT id FROM workspace LOOP
    PERFORM set_config('app.workspace_id', ws::text, true);
    DELETE FROM person_consent WHERE person_id IS NULL;
  END LOOP;
END $$;

ALTER TABLE person_consent DROP CONSTRAINT person_consent_lead_unique;
ALTER TABLE person_consent DROP CONSTRAINT person_consent_subject;
ALTER TABLE person_consent DROP COLUMN lead_id;
ALTER TABLE person_consent ALTER COLUMN person_id SET NOT NULL;