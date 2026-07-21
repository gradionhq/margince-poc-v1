-- 0107: Ratify ADR-0066's owner-private, progressive Voice DNA lifecycle.
-- Existing profile/corpus rows are preserved and translated into the closed
-- V1 vocabulary before the stricter constraints are installed.

ALTER TABLE voice_profile
  ADD COLUMN team_id uuid NULL,
  ADD COLUMN auto_learning_enabled boolean NOT NULL DEFAULT false,
  ADD COLUMN active_source_hash text NULL,
  ADD COLUMN last_built_at timestamptz NULL,
  ADD COLUMN source text NOT NULL DEFAULT 'ui',
  ADD COLUMN captured_by text NOT NULL DEFAULT 'system';

ALTER TABLE voice_profile DROP CONSTRAINT voice_profile_owner_fkey;
ALTER TABLE voice_profile
  ADD CONSTRAINT voice_profile_owner_fkey FOREIGN KEY (workspace_id, owner_id)
    REFERENCES app_user (workspace_id, id) ON DELETE RESTRICT;
ALTER TABLE voice_profile
  ADD CONSTRAINT voice_profile_team_fkey FOREIGN KEY (workspace_id, team_id)
    REFERENCES team (workspace_id, id) ON DELETE RESTRICT;

ALTER TABLE voice_profile DROP CONSTRAINT voice_profile_status_check;
UPDATE voice_profile
SET status = 'collecting'
WHERE status = 'building';

-- The old contract admitted team scope without a team identifier. There is no
-- faithful team mapping to invent during an upgrade, so quarantine those live
-- rows instead of widening private style material to workspace visibility.
UPDATE voice_profile
SET scope = 'workspace', owner_id = NULL, archived_at = coalesce(archived_at, now())
WHERE scope = 'team';

UPDATE voice_profile SET owner_id = NULL WHERE scope = 'workspace';
UPDATE voice_profile
SET scope = 'workspace', archived_at = coalesce(archived_at, now())
WHERE scope = 'user' AND owner_id IS NULL;

ALTER TABLE voice_profile
  ALTER COLUMN status SET DEFAULT 'collecting',
  ADD CONSTRAINT voice_profile_status_check CHECK (status IN ('collecting','ready','stale')),
  ADD CONSTRAINT voice_profile_scope_owner_check CHECK (
    (scope = 'user' AND owner_id IS NOT NULL AND team_id IS NULL) OR
    (scope = 'team' AND owner_id IS NULL AND team_id IS NOT NULL) OR
    (scope = 'workspace' AND owner_id IS NULL AND team_id IS NULL)
  ),
  ADD CONSTRAINT voice_profile_version_nonnegative CHECK (profile_version >= 0);

CREATE INDEX voice_profile_team_fk ON voice_profile(team_id) WHERE team_id IS NOT NULL;
CREATE INDEX voice_profile_owner_fk ON voice_profile(owner_id) WHERE owner_id IS NOT NULL;

ALTER TABLE voice_corpus_source
  ADD COLUMN origin text NOT NULL DEFAULT 'manual',
  ADD COLUMN content_hash text NULL,
  ADD COLUMN exclusion_reason text NULL,
  ADD COLUMN extractor_version text NOT NULL DEFAULT 'voice-v1',
  ADD COLUMN occurred_at timestamptz NULL,
  ADD COLUMN retention_until timestamptz NULL,
  ADD COLUMN content_erased_at timestamptz NULL,
  ADD COLUMN source text NOT NULL DEFAULT 'ui',
  ADD COLUMN captured_by text NOT NULL DEFAULT 'system',
  ADD COLUMN version bigint NOT NULL DEFAULT 1,
  ADD COLUMN archived_at timestamptz NULL;

ALTER TABLE voice_corpus_source
  DROP CONSTRAINT voice_corpus_source_kind_check,
  DROP CONSTRAINT voice_corpus_source_register_check,
  DROP CONSTRAINT voice_corpus_source_weight_check;

UPDATE voice_corpus_source
SET content_hash = 'sha256:' || encode(sha256(convert_to(content, 'UTF8')), 'hex'),
    occurred_at = created_at,
    exclusion_reason = CASE WHEN excluded THEN 'owner_excluded' ELSE NULL END,
    kind = CASE kind
      WHEN 'post' THEN 'linkedin'
      WHEN 'longform' THEN 'document'
      WHEN 'voice_memo' THEN 'other'
      WHEN 'chat' THEN 'other'
      ELSE kind
    END,
    register = CASE register
      WHEN 'spoken' THEN 'spoken'
      WHEN 'formal' THEN 'long_form'
      WHEN 'casual' THEN 'general'
      WHEN 'written' THEN CASE kind
        WHEN 'email' THEN 'email'
        WHEN 'post' THEN 'social'
        WHEN 'longform' THEN 'long_form'
        ELSE 'general'
      END
      ELSE 'general'
    END,
    weight = least(weight, 2.0);

ALTER TABLE voice_corpus_source
  ALTER COLUMN content_hash SET NOT NULL,
  ALTER COLUMN occurred_at SET NOT NULL,
  ALTER COLUMN content DROP NOT NULL,
  ALTER COLUMN weight TYPE numeric(4,3);

ALTER TABLE voice_corpus_source
  ADD CONSTRAINT voice_corpus_source_origin_check CHECK (origin IN ('manual','capture','draft_signal')),
  ADD CONSTRAINT voice_corpus_source_kind_check CHECK (kind IN ('email','linkedin','proposal','transcript','document','other')),
  ADD CONSTRAINT voice_corpus_source_register_check CHECK (register IN ('email','social','long_form','spoken','general')),
  ADD CONSTRAINT voice_corpus_source_weight_check CHECK (weight >= 0 AND weight <= 2),
  ADD CONSTRAINT voice_corpus_source_exclusion_check CHECK ((excluded AND exclusion_reason IS NOT NULL) OR NOT excluded);

CREATE INDEX voice_corpus_source_profile_fk ON voice_corpus_source(voice_profile_id);
CREATE INDEX voice_corpus_source_manifest
  ON voice_corpus_source(workspace_id, voice_profile_id, created_at DESC, id DESC)
  WHERE archived_at IS NULL;

CREATE TABLE voice_build (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  voice_profile_id uuid NOT NULL,
  requested_by uuid NULL,
  reason text NOT NULL CHECK (reason IN ('onboarding','manual','automatic')),
  status text NOT NULL CHECK (status IN ('queued','deferred','running','succeeded','failed')),
  stage text NULL CHECK (stage IN ('snapshot','extract','evaluate','activate')),
  source_hash text NOT NULL,
  source_count integer NOT NULL CHECK (source_count >= 0),
  result_version integer NULL CHECK (result_version >= 1),
  candidate_action text NOT NULL DEFAULT 'none'
    CHECK (candidate_action IN ('none','auto_activated','review_required')),
  status_code text NULL CHECK (status_code IN
    ('budget_deferred','model_unavailable','invalid_output','quality_regression','material_drift','internal')),
  status_detail text NULL,
  next_attempt_at timestamptz NULL,
  started_at timestamptz NULL,
  completed_at timestamptz NULL,
  version bigint NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NULL,
  archived_at timestamptz NULL,
  CONSTRAINT uq_voice_build_ws_id UNIQUE (workspace_id, id),
  CONSTRAINT voice_build_profile_fkey FOREIGN KEY (workspace_id, voice_profile_id)
    REFERENCES voice_profile(workspace_id, id) ON DELETE CASCADE,
  CONSTRAINT voice_build_requester_fkey FOREIGN KEY (workspace_id, requested_by)
    REFERENCES app_user(workspace_id, id) ON DELETE SET NULL (requested_by),
  CONSTRAINT voice_build_deferral_check CHECK (
    (status = 'deferred' AND status_code = 'budget_deferred' AND next_attempt_at IS NOT NULL) OR
    (status <> 'deferred' AND status_code IS DISTINCT FROM 'budget_deferred' AND next_attempt_at IS NULL)
  )
);
CREATE UNIQUE INDEX voice_build_one_active
  ON voice_build(workspace_id, voice_profile_id)
  WHERE status IN ('queued','deferred','running');
CREATE INDEX voice_build_profile_fk ON voice_build(voice_profile_id);
CREATE INDEX voice_build_requester_fk ON voice_build(requested_by) WHERE requested_by IS NOT NULL;
CREATE INDEX voice_build_poll
  ON voice_build(workspace_id, voice_profile_id, created_at DESC, id DESC)
  WHERE archived_at IS NULL;
CREATE INDEX voice_build_deferred_due
  ON voice_build(workspace_id, next_attempt_at, id)
  WHERE status = 'deferred' AND archived_at IS NULL;

CREATE TABLE voice_profile_version (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  voice_profile_id uuid NOT NULL,
  profile_version integer NOT NULL CHECK (profile_version >= 1),
  status text NOT NULL CHECK (status IN ('candidate','active','superseded','rejected')),
  voice_profile_md text NOT NULL,
  profile_json jsonb NOT NULL,
  stats_json jsonb NOT NULL,
  source_hash text NOT NULL,
  source_count integer NOT NULL CHECK (source_count >= 0),
  reason text NOT NULL CHECK (reason IN ('onboarding','manual','automatic','rollback')),
  predecessor_version integer NULL CHECK (predecessor_version >= 1),
  model_provider text NOT NULL,
  model_name text NOT NULL,
  builder_version text NOT NULL,
  activation_policy_version text NOT NULL,
  evaluation_json jsonb NOT NULL,
  review_reasons text[] NOT NULL DEFAULT '{}',
  activated_at timestamptz NULL,
  source text NOT NULL,
  captured_by text NOT NULL,
  version bigint NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NULL,
  archived_at timestamptz NULL,
  CONSTRAINT uq_voice_profile_version_ws_id UNIQUE (workspace_id, id),
  CONSTRAINT uq_voice_profile_version_number UNIQUE (workspace_id, voice_profile_id, profile_version),
  CONSTRAINT voice_profile_version_profile_fkey FOREIGN KEY (workspace_id, voice_profile_id)
    REFERENCES voice_profile(workspace_id, id) ON DELETE CASCADE,
  CONSTRAINT voice_profile_version_predecessor_fkey
    FOREIGN KEY (workspace_id, voice_profile_id, predecessor_version)
    REFERENCES voice_profile_version(workspace_id, voice_profile_id, profile_version)
);

-- A previously built artifact is already the user's last known-good version.
-- Ratification must make that history explicit rather than treating the next
-- build as version N+1 with a missing predecessor.
UPDATE voice_profile p
SET active_source_hash = (
      SELECT md5(coalesce(string_agg(s.content_hash, ',' ORDER BY s.source_ref), ''))
      FROM voice_corpus_source s
      WHERE s.voice_profile_id = p.id AND NOT s.excluded
    ),
    last_built_at = coalesce(p.updated_at, p.created_at)
WHERE p.profile_version >= 1;

INSERT INTO voice_profile_version
  (workspace_id, voice_profile_id, profile_version, status, voice_profile_md,
   profile_json, stats_json, source_hash, source_count, reason, predecessor_version,
   model_provider, model_name, builder_version, activation_policy_version,
   evaluation_json, review_reasons, activated_at, source, captured_by, updated_at)
SELECT p.workspace_id, p.id, p.profile_version, 'active', p.voice_profile_md,
       jsonb_build_object('document', p.voice_profile_md), '{}'::jsonb,
       p.active_source_hash,
       (SELECT count(*)::integer FROM voice_corpus_source s
        WHERE s.voice_profile_id = p.id AND NOT s.excluded),
       'manual', NULL, 'legacy', coalesce(p.model_ref, 'legacy-unrecorded'),
       'pre-adr-0066', 'legacy',
       jsonb_build_object(
         'held_out_prompts', 5,
         'repeats_per_prompt', 3,
         'active_median_voice_score', NULL,
         'candidate_median_voice_score', 1,
         'anti_ai_hard_failures', 0,
         'structured_output_valid', true,
         'corpus_citations_valid', true,
         'identity_word_jaccard', 1,
         'signature_set_jaccard', 1,
         'removed_avoid_rules', 0,
         'removed_register_rules', 0,
         'classification', 'routine',
         'passed', true
       ),
       '{}'::text[], p.last_built_at, p.source, p.captured_by, p.last_built_at
FROM voice_profile p
WHERE p.profile_version >= 1;

CREATE UNIQUE INDEX voice_profile_version_one_active
  ON voice_profile_version(workspace_id, voice_profile_id) WHERE status = 'active';
CREATE INDEX voice_profile_version_profile_fk ON voice_profile_version(voice_profile_id);
CREATE INDEX voice_profile_version_history
  ON voice_profile_version(workspace_id, voice_profile_id, created_at DESC, id DESC)
  WHERE archived_at IS NULL;

ALTER TABLE voice_build
  ADD CONSTRAINT voice_build_result_version_fkey
  FOREIGN KEY (workspace_id, voice_profile_id, result_version)
  REFERENCES voice_profile_version(workspace_id, voice_profile_id, profile_version);

CREATE TABLE voice_profile_delta (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  voice_profile_id uuid NOT NULL,
  from_version integer NULL CHECK (from_version >= 1),
  to_version integer NOT NULL CHECK (to_version >= 1),
  classification text NOT NULL CHECK (classification IN ('routine','material')),
  activation_outcome text NOT NULL CHECK (activation_outcome IN
    ('auto_activated','review_required','manually_activated','rejected','rollback')),
  delta_json jsonb NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NULL,
  archived_at timestamptz NULL,
  CONSTRAINT uq_voice_profile_delta_ws_id UNIQUE (workspace_id, id),
  CONSTRAINT uq_voice_profile_delta_version UNIQUE (workspace_id, voice_profile_id, to_version),
  CONSTRAINT voice_profile_delta_profile_fkey FOREIGN KEY (workspace_id, voice_profile_id)
    REFERENCES voice_profile(workspace_id, id) ON DELETE CASCADE,
  CONSTRAINT voice_profile_delta_from_fkey FOREIGN KEY (workspace_id, voice_profile_id, from_version)
    REFERENCES voice_profile_version(workspace_id, voice_profile_id, profile_version),
  CONSTRAINT voice_profile_delta_to_fkey FOREIGN KEY (workspace_id, voice_profile_id, to_version)
    REFERENCES voice_profile_version(workspace_id, voice_profile_id, profile_version)
);
CREATE INDEX voice_profile_delta_profile_fk ON voice_profile_delta(voice_profile_id);
CREATE INDEX voice_profile_delta_history
  ON voice_profile_delta(workspace_id, voice_profile_id, created_at DESC, id DESC)
  WHERE archived_at IS NULL;

CREATE TABLE voice_learning_signal (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  voice_profile_id uuid NOT NULL,
  profile_version integer NULL CHECK (profile_version >= 1),
  draft_ref_hash bytea NOT NULL,
  outcome text NOT NULL CHECK (outcome IN ('drafted','accepted','edited_sent','rejected')),
  generated_original text NULL,
  final_text text NULL,
  final_captured_by text NULL,
  qualifies_as_source boolean NOT NULL DEFAULT false,
  similarity numeric(5,4) NULL CHECK (similarity >= 0 AND similarity <= 1),
  transformations jsonb NOT NULL DEFAULT '[]',
  retention_until timestamptz NOT NULL,
  content_erased_at timestamptz NULL,
  source text NOT NULL,
  captured_by text NOT NULL,
  version bigint NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NULL,
  archived_at timestamptz NULL,
  CONSTRAINT uq_voice_learning_signal_ws_id UNIQUE (workspace_id, id),
  CONSTRAINT uq_voice_learning_signal_draft UNIQUE (workspace_id, draft_ref_hash),
  CONSTRAINT voice_learning_signal_profile_fkey FOREIGN KEY (workspace_id, voice_profile_id)
    REFERENCES voice_profile(workspace_id, id) ON DELETE CASCADE,
  CONSTRAINT voice_learning_signal_version_fkey FOREIGN KEY (workspace_id, voice_profile_id, profile_version)
    REFERENCES voice_profile_version(workspace_id, voice_profile_id, profile_version),
  CONSTRAINT voice_learning_signal_qualifies_check CHECK (NOT qualifies_as_source OR
    (outcome = 'edited_sent' AND final_text IS NOT NULL AND final_captured_by LIKE 'human:%'))
);
CREATE INDEX voice_learning_signal_profile_fk ON voice_learning_signal(voice_profile_id);
CREATE INDEX voice_learning_signal_retention
  ON voice_learning_signal(workspace_id, retention_until)
  WHERE content_erased_at IS NULL;

ALTER TABLE voice_build ENABLE ROW LEVEL SECURITY;
ALTER TABLE voice_build FORCE ROW LEVEL SECURITY;
CREATE POLICY voice_build_tenant_isolation ON voice_build
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);

ALTER TABLE voice_profile_version ENABLE ROW LEVEL SECURITY;
ALTER TABLE voice_profile_version FORCE ROW LEVEL SECURITY;
CREATE POLICY voice_profile_version_tenant_isolation ON voice_profile_version
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);

ALTER TABLE voice_profile_delta ENABLE ROW LEVEL SECURITY;
ALTER TABLE voice_profile_delta FORCE ROW LEVEL SECURITY;
CREATE POLICY voice_profile_delta_tenant_isolation ON voice_profile_delta
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);

ALTER TABLE voice_learning_signal ENABLE ROW LEVEL SECURITY;
ALTER TABLE voice_learning_signal FORCE ROW LEVEL SECURITY;
CREATE POLICY voice_learning_signal_tenant_isolation ON voice_learning_signal
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);

-- Defaults above existed only to label pre-ratification rows during ALTER.
-- New writes must stamp their real provenance explicitly.
ALTER TABLE voice_profile
  ALTER COLUMN source DROP DEFAULT,
  ALTER COLUMN captured_by DROP DEFAULT;
ALTER TABLE voice_corpus_source
  ALTER COLUMN source DROP DEFAULT,
  ALTER COLUMN captured_by DROP DEFAULT;
