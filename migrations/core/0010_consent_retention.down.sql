ALTER TABLE lead         DROP COLUMN IF EXISTS legal_hold;
ALTER TABLE deal         DROP COLUMN IF EXISTS legal_hold;
ALTER TABLE organization DROP COLUMN IF EXISTS legal_hold;
ALTER TABLE person       DROP COLUMN IF EXISTS legal_hold;
DROP TABLE IF EXISTS retention_policy;
DROP TABLE IF EXISTS consent_event;
DROP TABLE IF EXISTS person_consent;
DROP TABLE IF EXISTS consent_purpose;
