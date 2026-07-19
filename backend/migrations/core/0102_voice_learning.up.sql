-- 0102: durable Voice DNA builds and learning controls. The live profile
-- remains the current pointer; immutable versions preserve every artifact
-- used to draft, while build rows make model work observable and retryable.

ALTER TABLE voice_profile
  ADD COLUMN auto_learning_enabled boolean NOT NULL DEFAULT false,
  ADD COLUMN active_source_hash text NOT NULL DEFAULT '',
  ADD COLUMN last_built_at timestamptz NULL;

ALTER TABLE voice_corpus_source
  ADD COLUMN origin text NOT NULL DEFAULT 'manual'
    CHECK (origin IN ('manual','capture','draft_signal')),
  ADD COLUMN exclusion_reason text NULL,
  ADD COLUMN content_hash text NOT NULL DEFAULT '',
  ADD COLUMN extractor_version integer NOT NULL DEFAULT 1,
  ADD COLUMN occurred_at timestamptz NULL;

UPDATE voice_corpus_source
SET content_hash = encode(sha256(convert_to(content, 'UTF8')), 'hex')
WHERE content_hash = '';

CREATE TABLE voice_profile_version (
  id                  uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id        uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  voice_profile_id    uuid NOT NULL,
  profile_version     integer NOT NULL,
  voice_profile_md    text NOT NULL,
  profile_json        jsonb NOT NULL,
  stats_json          jsonb NOT NULL,
  model_ref           text NULL,
  builder_version     integer NOT NULL,
  source_hash         text NOT NULL,
  source_word_count   integer NOT NULL CHECK (source_word_count >= 0),
  reason              text NOT NULL CHECK (reason IN ('onboarding','manual','automatic','rollback')),
  predecessor_version integer NULL,
  active              boolean NOT NULL DEFAULT false,
  created_at          timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT voice_profile_version_profile_fkey
    FOREIGN KEY (workspace_id, voice_profile_id)
    REFERENCES voice_profile (workspace_id, id) ON DELETE CASCADE,
  CONSTRAINT uq_voice_profile_version UNIQUE (workspace_id, voice_profile_id, profile_version)
);
CREATE INDEX idx_voice_profile_version_profile
  ON voice_profile_version (workspace_id, voice_profile_id, profile_version DESC);
CREATE UNIQUE INDEX uq_voice_profile_active_version
  ON voice_profile_version (workspace_id, voice_profile_id) WHERE active;

ALTER TABLE voice_profile_version ENABLE ROW LEVEL SECURITY;
ALTER TABLE voice_profile_version FORCE ROW LEVEL SECURITY;
CREATE POLICY voice_profile_version_tenant_isolation ON voice_profile_version
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);

CREATE TABLE voice_build (
  id               uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id     uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  voice_profile_id uuid NOT NULL,
  requested_by     uuid NOT NULL,
  reason           text NOT NULL CHECK (reason IN ('onboarding','manual','automatic')),
  status           text NOT NULL DEFAULT 'queued'
    CHECK (status IN ('queued','running','succeeded','failed')),
  stage            text NOT NULL DEFAULT 'queued',
  source_hash      text NOT NULL,
  source_word_count integer NOT NULL CHECK (source_word_count >= 0),
  result_version   integer NULL,
  failure_detail   text NULL,
  created_at       timestamptz NOT NULL DEFAULT now(),
  started_at       timestamptz NULL,
  finished_at      timestamptz NULL,
  CONSTRAINT voice_build_profile_fkey
    FOREIGN KEY (workspace_id, voice_profile_id)
    REFERENCES voice_profile (workspace_id, id) ON DELETE CASCADE,
  CONSTRAINT voice_build_requester_fkey
    FOREIGN KEY (workspace_id, requested_by)
    REFERENCES app_user (workspace_id, id) ON DELETE RESTRICT
);
CREATE INDEX idx_voice_build_profile
  ON voice_build (workspace_id, voice_profile_id, created_at DESC);
CREATE UNIQUE INDEX uq_voice_build_active
  ON voice_build (workspace_id, voice_profile_id)
  WHERE status IN ('queued','running');

ALTER TABLE voice_build ENABLE ROW LEVEL SECURITY;
ALTER TABLE voice_build FORCE ROW LEVEL SECURITY;
CREATE POLICY voice_build_tenant_isolation ON voice_build
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);

CREATE TABLE voice_profile_delta (
  id               uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id     uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  voice_profile_id uuid NOT NULL,
  from_version     integer NOT NULL,
  to_version       integer NOT NULL,
  summary_json     jsonb NOT NULL,
  created_at       timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT voice_profile_delta_profile_fkey
    FOREIGN KEY (workspace_id, voice_profile_id)
    REFERENCES voice_profile (workspace_id, id) ON DELETE CASCADE
);
CREATE INDEX idx_voice_profile_delta_profile
  ON voice_profile_delta (workspace_id, voice_profile_id, created_at DESC);

ALTER TABLE voice_profile_delta ENABLE ROW LEVEL SECURITY;
ALTER TABLE voice_profile_delta FORCE ROW LEVEL SECURITY;
CREATE POLICY voice_profile_delta_tenant_isolation ON voice_profile_delta
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);

CREATE TABLE voice_learning_signal (
  id               uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id     uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  voice_profile_id uuid NOT NULL,
  draft_ref        text NOT NULL,
  profile_version  integer NOT NULL,
  outcome          text NOT NULL CHECK (outcome IN ('drafted','edited_sent','accepted','rejected')),
  original_text    text NOT NULL,
  final_text       text NULL,
  similarity       numeric(5,4) NULL CHECK (similarity >= 0 AND similarity <= 1),
  transformations  jsonb NOT NULL DEFAULT '[]'::jsonb,
  active           boolean NOT NULL DEFAULT true,
  created_at       timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT voice_learning_signal_profile_fkey
    FOREIGN KEY (workspace_id, voice_profile_id)
    REFERENCES voice_profile (workspace_id, id) ON DELETE CASCADE,
  CONSTRAINT uq_voice_learning_signal_draft UNIQUE (workspace_id, voice_profile_id, draft_ref)
);
CREATE INDEX idx_voice_learning_signal_profile
  ON voice_learning_signal (workspace_id, voice_profile_id, created_at DESC);

ALTER TABLE voice_learning_signal ENABLE ROW LEVEL SECURITY;
ALTER TABLE voice_learning_signal FORCE ROW LEVEL SECURITY;
CREATE POLICY voice_learning_signal_tenant_isolation ON voice_learning_signal
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);
