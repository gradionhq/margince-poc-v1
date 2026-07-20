DROP TABLE voice_learning_signal;
DROP TABLE voice_profile_delta;
ALTER TABLE voice_build DROP CONSTRAINT voice_build_result_version_fkey;
DROP TABLE voice_profile_version;
DROP TABLE voice_build;

DROP INDEX voice_corpus_source_manifest;
DROP INDEX voice_corpus_source_profile_fk;
ALTER TABLE voice_corpus_source
  DROP CONSTRAINT voice_corpus_source_exclusion_check,
  DROP CONSTRAINT voice_corpus_source_weight_check,
  DROP CONSTRAINT voice_corpus_source_register_check,
  DROP CONSTRAINT voice_corpus_source_kind_check,
  DROP CONSTRAINT voice_corpus_source_origin_check;

UPDATE voice_corpus_source
SET kind = CASE kind
      WHEN 'linkedin' THEN 'post'
      WHEN 'document' THEN 'longform'
      WHEN 'proposal' THEN 'longform'
      WHEN 'other' THEN 'longform'
      ELSE kind
    END,
    register = CASE register
      WHEN 'spoken' THEN 'spoken'
      WHEN 'social' THEN 'written'
      WHEN 'long_form' THEN 'formal'
      WHEN 'email' THEN 'written'
      ELSE 'casual'
    END,
    weight = greatest(weight, 0.1),
    excluded = excluded OR content IS NULL,
    content = coalesce(content, '');

ALTER TABLE voice_corpus_source
  ALTER COLUMN content SET NOT NULL,
  ALTER COLUMN weight TYPE numeric(2,1),
  ADD CONSTRAINT voice_corpus_source_kind_check CHECK (kind IN ('post','transcript','email','chat','longform','voice_memo')),
  ADD CONSTRAINT voice_corpus_source_register_check CHECK (register IN ('spoken','written','casual','formal')),
  ADD CONSTRAINT voice_corpus_source_weight_check CHECK (weight >= 0.1 AND weight <= 5.0),
  DROP COLUMN archived_at,
  DROP COLUMN version,
  DROP COLUMN captured_by,
  DROP COLUMN source,
  DROP COLUMN content_erased_at,
  DROP COLUMN retention_until,
  DROP COLUMN occurred_at,
  DROP COLUMN extractor_version,
  DROP COLUMN exclusion_reason,
  DROP COLUMN content_hash,
  DROP COLUMN origin;

DROP INDEX voice_profile_owner_fk;
DROP INDEX voice_profile_team_fk;
ALTER TABLE voice_profile
  DROP CONSTRAINT voice_profile_version_nonnegative,
  DROP CONSTRAINT voice_profile_scope_owner_check,
  DROP CONSTRAINT voice_profile_status_check,
  DROP CONSTRAINT voice_profile_owner_fkey;
UPDATE voice_profile SET status = 'building' WHERE status = 'collecting';
ALTER TABLE voice_profile
  ALTER COLUMN status SET DEFAULT 'building',
  ADD CONSTRAINT voice_profile_status_check CHECK (status IN ('building','ready','stale')),
  ADD CONSTRAINT voice_profile_owner_fkey FOREIGN KEY (workspace_id, owner_id)
    REFERENCES app_user (workspace_id, id) ON DELETE SET NULL (owner_id),
  DROP CONSTRAINT voice_profile_team_fkey,
  DROP COLUMN captured_by,
  DROP COLUMN source,
  DROP COLUMN last_built_at,
  DROP COLUMN active_source_hash,
  DROP COLUMN auto_learning_enabled,
  DROP COLUMN team_id;
