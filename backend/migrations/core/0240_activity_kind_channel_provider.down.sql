ALTER TABLE person_channel_identity DROP CONSTRAINT person_channel_identity_provider_fkey;
ALTER TABLE person_channel_identity ADD CONSTRAINT person_channel_identity_provider_check
  CHECK (provider IN ('telegram'));

ALTER TABLE activity DROP CONSTRAINT activity_kind_fkey;
ALTER TABLE activity ADD CONSTRAINT activity_kind_check
  CHECK (kind IN ('email','call','meeting','note','task','whatsapp','telegram'));

DROP TABLE channel_provider;
DROP TABLE activity_kind;
