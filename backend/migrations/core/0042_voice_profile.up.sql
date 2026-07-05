-- 0042: Voice DNA storage (B-E07.4, data-model §12.5 extended to the
-- features/09 §B0.2 shape). Two entities: `voice_profile` — the machine-
-- derived `voice_profile_md` artifact (versioned by `profile_version`,
-- rewritten wholesale on every rebuild) kept strictly apart from the
-- human-authored `personality_md` (free text a rebuild never touches) —
-- and `voice_corpus_source`, the per-source corpus manifest (kind,
-- register, weight, label, word count, opt-out flag) plus the ingested
-- text itself, which the profile builder reads.
--
-- PII posture (a domain judgment, mirrored in piicoverage_test.go's own
-- commentary): the corpus holds the OWNER's consented writing samples —
-- ingest speaker-filters transcripts to the owner's own turns (features/09
-- §B1.2), so a data subject's words never enter by construction. It is
-- therefore outside the Art. 17 subject-erasure registry, like the
-- person-referencing consent proof logs.
CREATE TABLE voice_profile (
  id               uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id     uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  owner_id         uuid NULL,                              -- null = workspace/team profile (§12.5)
  scope            text NOT NULL DEFAULT 'user' CHECK (scope IN ('user','team','workspace')),
  model_ref        text NULL,                              -- derived style descriptor / embedding ref
  status           text NOT NULL DEFAULT 'building' CHECK (status IN ('building','ready','stale')),
  voice_profile_md text NOT NULL DEFAULT '',               -- DERIVED artifact (features/09 §B0.2 fixed schema)
  profile_version  integer NOT NULL DEFAULT 0,             -- bumps on every derived rebuild; 0 = never built
  personality_md   text NOT NULL DEFAULT '',               -- HUMAN-authored identity; rebuilds never write it
  version          bigint NOT NULL DEFAULT 1,
  created_at       timestamptz NOT NULL DEFAULT now(),
  updated_at       timestamptz NULL,
  archived_at      timestamptz NULL,
  CONSTRAINT uq_voice_profile_ws_id UNIQUE (workspace_id, id),
  CONSTRAINT voice_profile_owner_fkey FOREIGN KEY (workspace_id, owner_id)
    REFERENCES app_user (workspace_id, id) ON DELETE SET NULL (owner_id),
  -- A derived artifact without a version would be unauditable: which
  -- rebuild produced the text the drafts are riding?
  CONSTRAINT voice_profile_derived_versioned CHECK (voice_profile_md = '' OR profile_version >= 1)
);
-- One live personal profile per user: onboarding and voice.html resume
-- THE profile, they never fork a second one.
CREATE UNIQUE INDEX uq_voice_profile_user_live ON voice_profile (workspace_id, owner_id)
  WHERE scope = 'user' AND archived_at IS NULL;

ALTER TABLE voice_profile ENABLE ROW LEVEL SECURITY;
ALTER TABLE voice_profile FORCE ROW LEVEL SECURITY;
CREATE POLICY voice_profile_tenant_isolation ON voice_profile
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);

-- The corpus manifest (features/09 §B0.2, superseding the §12.5
-- sample_kind/sample_ref/excluded stub). `source_ref` is the source's
-- natural key (message id, upload name, transcript id — or a content
-- hash when the caller has none): ingest is idempotent per source, a
-- re-ingest replaces the row instead of double-counting the meter.
CREATE TABLE voice_corpus_source (
  id               uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id     uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  voice_profile_id uuid NOT NULL,
  kind             text NOT NULL CHECK (kind IN ('post','transcript','email','chat','longform','voice_memo')),
  register         text NOT NULL CHECK (register IN ('spoken','written','casual','formal')),
  weight           numeric(2,1) NOT NULL DEFAULT 1.0 CHECK (weight >= 0.1 AND weight <= 5.0),
  source_label     text NOT NULL,
  source_ref       text NOT NULL,
  content          text NOT NULL,                          -- speaker-filtered text the builder reads
  word_count       integer NOT NULL CHECK (word_count >= 0),
  excluded         boolean NOT NULL DEFAULT false,         -- manifest opt-out; excluded rows leave the meter
  created_at       timestamptz NOT NULL DEFAULT now(),
  updated_at       timestamptz NULL,
  CONSTRAINT voice_corpus_source_profile_fkey FOREIGN KEY (workspace_id, voice_profile_id)
    REFERENCES voice_profile (workspace_id, id) ON DELETE CASCADE,
  CONSTRAINT uq_voice_corpus_source_ref UNIQUE (workspace_id, voice_profile_id, source_ref)
);
CREATE INDEX idx_voice_corpus_profile ON voice_corpus_source (workspace_id, voice_profile_id, created_at DESC);

ALTER TABLE voice_corpus_source ENABLE ROW LEVEL SECURITY;
ALTER TABLE voice_corpus_source FORCE ROW LEVEL SECURITY;
CREATE POLICY voice_corpus_source_tenant_isolation ON voice_corpus_source
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);

-- Backfill the `voice_profile` RBAC object into the seeded system-role
-- policy documents of EXISTING workspaces (new workspaces get it from
-- the code-side seed). Posture: a voice is personal working material —
-- reps create and maintain their own (no delete, like their records);
-- managers/admin/ops carry delete for team hygiene.
UPDATE role SET permissions = jsonb_set(
  permissions, '{objects,voice_profile}',
  '{"create":true,"read":true,"update":true,"delete":true}'::jsonb)
WHERE is_system AND key IN ('admin','ops','manager')
  AND NOT permissions->'objects' ? 'voice_profile';
UPDATE role SET permissions = jsonb_set(
  permissions, '{objects,voice_profile}',
  '{"create":true,"read":true,"update":true,"delete":false}'::jsonb)
WHERE is_system AND key = 'rep'
  AND NOT permissions->'objects' ? 'voice_profile';
UPDATE role SET permissions = jsonb_set(
  permissions, '{objects,voice_profile}',
  '{"create":false,"read":true,"update":false,"delete":false}'::jsonb)
WHERE is_system AND key = 'read_only'
  AND NOT permissions->'objects' ? 'voice_profile';
