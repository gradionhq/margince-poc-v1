-- 0234: the voice tables drop the tenant column (ADR-0091 §8 phase D).
--
-- Six tables, seven indexes, five redundant uniques. The module writes none of
-- the column's names in Go — every voice write goes through storekit's shape —
-- so this slice is schema plus the fixtures that seeded a profile by hand.

DROP INDEX idx_voice_corpus_profile;
CREATE INDEX idx_voice_corpus_profile ON voice_corpus_source (voice_profile_id, created_at DESC);

DROP INDEX voice_build_deferred_due;
CREATE INDEX voice_build_deferred_due ON voice_build (next_attempt_at, id) WHERE status = 'deferred' AND archived_at IS NULL;

DROP INDEX voice_build_poll;
CREATE INDEX voice_build_poll ON voice_build (voice_profile_id, created_at DESC, id DESC) WHERE archived_at IS NULL;

DROP INDEX voice_corpus_source_manifest;
CREATE INDEX voice_corpus_source_manifest ON voice_corpus_source (voice_profile_id, created_at DESC, id DESC) WHERE archived_at IS NULL;

DROP INDEX voice_learning_signal_retention;
CREATE INDEX voice_learning_signal_retention ON voice_learning_signal (retention_until) WHERE content_erased_at IS NULL;

DROP INDEX voice_profile_delta_history;
CREATE INDEX voice_profile_delta_history ON voice_profile_delta (voice_profile_id, created_at DESC, id DESC) WHERE archived_at IS NULL;

DROP INDEX voice_profile_version_history;
CREATE INDEX voice_profile_version_history ON voice_profile_version (voice_profile_id, created_at DESC, id DESC) WHERE archived_at IS NULL;

ALTER TABLE voice_profile DROP CONSTRAINT uq_voice_profile_ws_id;
ALTER TABLE voice_build DROP CONSTRAINT uq_voice_build_ws_id;
ALTER TABLE voice_learning_signal DROP CONSTRAINT uq_voice_learning_signal_ws_id;
ALTER TABLE voice_profile_delta DROP CONSTRAINT uq_voice_profile_delta_ws_id;
ALTER TABLE voice_profile_version DROP CONSTRAINT uq_voice_profile_version_ws_id;

ALTER TABLE voice_profile DROP COLUMN workspace_id;
ALTER TABLE voice_corpus_source DROP COLUMN workspace_id;
ALTER TABLE voice_build DROP COLUMN workspace_id;
ALTER TABLE voice_profile_version DROP COLUMN workspace_id;
ALTER TABLE voice_profile_delta DROP COLUMN workspace_id;
ALTER TABLE voice_learning_signal DROP COLUMN workspace_id;
