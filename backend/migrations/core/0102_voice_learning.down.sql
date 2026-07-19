DROP TABLE voice_learning_signal;
DROP TABLE voice_profile_delta;
DROP TABLE voice_build;
DROP TABLE voice_profile_version;

ALTER TABLE voice_corpus_source
  DROP COLUMN occurred_at,
  DROP COLUMN extractor_version,
  DROP COLUMN content_hash,
  DROP COLUMN exclusion_reason,
  DROP COLUMN origin;

ALTER TABLE voice_profile
  DROP COLUMN last_built_at,
  DROP COLUMN active_source_hash,
  DROP COLUMN auto_learning_enabled;
