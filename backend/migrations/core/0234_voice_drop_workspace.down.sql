-- Reverse of 0234: the six voice tables carry the tenant column again.
--
-- The backfill reads the LIVE workspace, and the predicate is the point: 0217's
-- pre-flight refuses to run against a database holding more than one workspace
-- with archived_at IS NULL, so there is exactly one live row — but an
-- installation that resolved to one organization by ARCHIVING the others still
-- has those rows, and 0217 names that residue explicitly. Ordering by
-- created_at alone would hand every restored row to whichever workspace
-- happened to be created first, archived or not.
--
-- If no live workspace exists and a table is not empty, SET NOT NULL fails and
-- the rollback stops — the honest outcome, since no value this migration could
-- write would be true.

ALTER TABLE voice_profile ADD COLUMN workspace_id uuid;
ALTER TABLE voice_corpus_source ADD COLUMN workspace_id uuid;
ALTER TABLE voice_build ADD COLUMN workspace_id uuid;
ALTER TABLE voice_profile_version ADD COLUMN workspace_id uuid;
ALTER TABLE voice_profile_delta ADD COLUMN workspace_id uuid;
ALTER TABLE voice_learning_signal ADD COLUMN workspace_id uuid;

DO $$
DECLARE ws uuid := (SELECT id FROM workspace WHERE archived_at IS NULL ORDER BY created_at LIMIT 1);
BEGIN
  UPDATE voice_profile SET workspace_id = ws;
  UPDATE voice_corpus_source SET workspace_id = ws;
  UPDATE voice_build SET workspace_id = ws;
  UPDATE voice_profile_version SET workspace_id = ws;
  UPDATE voice_profile_delta SET workspace_id = ws;
  UPDATE voice_learning_signal SET workspace_id = ws;
END $$;

ALTER TABLE voice_profile ALTER COLUMN workspace_id SET NOT NULL;
ALTER TABLE voice_corpus_source ALTER COLUMN workspace_id SET NOT NULL;
ALTER TABLE voice_build ALTER COLUMN workspace_id SET NOT NULL;
ALTER TABLE voice_profile_version ALTER COLUMN workspace_id SET NOT NULL;
ALTER TABLE voice_profile_delta ALTER COLUMN workspace_id SET NOT NULL;
ALTER TABLE voice_learning_signal ALTER COLUMN workspace_id SET NOT NULL;

ALTER TABLE voice_profile ADD CONSTRAINT voice_profile_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE voice_corpus_source ADD CONSTRAINT voice_corpus_source_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE voice_build ADD CONSTRAINT voice_build_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE voice_profile_version ADD CONSTRAINT voice_profile_version_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE voice_profile_delta ADD CONSTRAINT voice_profile_delta_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE voice_learning_signal ADD CONSTRAINT voice_learning_signal_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;

ALTER TABLE voice_profile ADD CONSTRAINT uq_voice_profile_ws_id UNIQUE (id);
ALTER TABLE voice_build ADD CONSTRAINT uq_voice_build_ws_id UNIQUE (id);
ALTER TABLE voice_learning_signal ADD CONSTRAINT uq_voice_learning_signal_ws_id UNIQUE (id);
ALTER TABLE voice_profile_delta ADD CONSTRAINT uq_voice_profile_delta_ws_id UNIQUE (id);
ALTER TABLE voice_profile_version ADD CONSTRAINT uq_voice_profile_version_ws_id UNIQUE (id);

DROP INDEX idx_voice_corpus_profile;
CREATE INDEX idx_voice_corpus_profile ON voice_corpus_source (workspace_id, voice_profile_id, created_at DESC);

DROP INDEX voice_build_deferred_due;
CREATE INDEX voice_build_deferred_due ON voice_build (workspace_id, next_attempt_at, id) WHERE status = 'deferred' AND archived_at IS NULL;

DROP INDEX voice_build_poll;
CREATE INDEX voice_build_poll ON voice_build (workspace_id, voice_profile_id, created_at DESC, id DESC) WHERE archived_at IS NULL;

DROP INDEX voice_corpus_source_manifest;
CREATE INDEX voice_corpus_source_manifest ON voice_corpus_source (workspace_id, voice_profile_id, created_at DESC, id DESC) WHERE archived_at IS NULL;

DROP INDEX voice_learning_signal_retention;
CREATE INDEX voice_learning_signal_retention ON voice_learning_signal (workspace_id, retention_until) WHERE content_erased_at IS NULL;

DROP INDEX voice_profile_delta_history;
CREATE INDEX voice_profile_delta_history ON voice_profile_delta (workspace_id, voice_profile_id, created_at DESC, id DESC) WHERE archived_at IS NULL;

DROP INDEX voice_profile_version_history;
CREATE INDEX voice_profile_version_history ON voice_profile_version (workspace_id, voice_profile_id, created_at DESC, id DESC) WHERE archived_at IS NULL;

