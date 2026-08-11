-- Reverse of 0209. Tables first, then the columns added to existing ones.
--
-- The seeded business_correspondence purpose rows are deliberately NOT deleted:
-- a workspace that recorded consent decisions against that purpose would lose
-- the proof along with the row, and consent_event references the purpose with
-- ON DELETE RESTRICT precisely so that cannot happen quietly. Dropping the
-- class column returns every purpose to the single default-deny gate, which is
-- the pre-0207 behaviour whether the row is there or not.

DROP TABLE IF EXISTS consent_existing_customer_flag;
DROP TABLE IF EXISTS consent_qualifying_event;
DROP TABLE IF EXISTS person_moment_dismissal;
DROP TABLE IF EXISTS person_brief;
DROP TABLE IF EXISTS conversation_claim;

ALTER TABLE consent_event DROP COLUMN IF EXISTS confirm_user_agent;
ALTER TABLE consent_event DROP COLUMN IF EXISTS confirm_ip;
ALTER TABLE consent_event DROP COLUMN IF EXISTS issuance_trigger;

ALTER TABLE consent_purpose DROP COLUMN IF EXISTS class;

ALTER TABLE person DROP COLUMN IF EXISTS photo_origin;
ALTER TABLE person DROP COLUMN IF EXISTS photo_object_key;
