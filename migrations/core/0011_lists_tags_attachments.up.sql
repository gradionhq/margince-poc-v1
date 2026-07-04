-- Lists, tags, attachments (data-model §10). record_grant (§2.5) rides
-- here too: it needs no domain FKs but arrives after the entities it names.

CREATE TABLE list (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  name         text NOT NULL,
  entity_type  text NOT NULL CHECK (entity_type IN ('person','organization','deal','lead')),
  list_type    text NOT NULL DEFAULT 'static' CHECK (list_type IN ('static','dynamic')),
  definition   jsonb NULL,          -- dynamic: the validated query-plan; static: NULL
  owner_id     uuid NULL REFERENCES app_user(id) ON DELETE SET NULL,
  team_id      uuid NULL REFERENCES team(id) ON DELETE SET NULL,
  version      bigint NOT NULL DEFAULT 1,
  created_at   timestamptz NOT NULL DEFAULT now(),
  updated_at   timestamptz NOT NULL DEFAULT now(),
  archived_at  timestamptz NULL
);
CREATE TRIGGER trg_list_updated BEFORE UPDATE ON list
  FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();

CREATE TABLE list_member (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  list_id      uuid NOT NULL REFERENCES list(id) ON DELETE CASCADE,
  entity_type  text NOT NULL CHECK (entity_type IN ('person','organization','deal','lead')),
  entity_id    uuid NOT NULL,       -- polymorphic by (entity_type, entity_id); integrity by app + §1.10 cleanup
  added_by     text NOT NULL,
  created_at   timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT list_member_unique UNIQUE (list_id, entity_type, entity_id)
);
CREATE INDEX idx_list_member_list   ON list_member (list_id);
CREATE INDEX idx_list_member_entity ON list_member (workspace_id, entity_type, entity_id);

CREATE TABLE tag (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  name         text NOT NULL,
  color        text NULL,
  version      bigint NOT NULL DEFAULT 1,
  created_at   timestamptz NOT NULL DEFAULT now(),
  updated_at   timestamptz NOT NULL DEFAULT now(),
  archived_at  timestamptz NULL
);
CREATE UNIQUE INDEX uq_tag_name ON tag (workspace_id, lower(name));
CREATE TRIGGER trg_tag_updated BEFORE UPDATE ON tag
  FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();

CREATE TABLE taggable (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  tag_id       uuid NOT NULL REFERENCES tag(id) ON DELETE CASCADE,
  entity_type  text NOT NULL CHECK (entity_type IN ('person','organization','deal','lead')),
  entity_id    uuid NOT NULL,
  created_at   timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT taggable_unique UNIQUE (tag_id, entity_type, entity_id)
);
CREATE INDEX idx_taggable_entity ON taggable (workspace_id, entity_type, entity_id);
CREATE INDEX idx_taggable_tag    ON taggable (tag_id);

-- Files live in object storage; the DB stores references + metadata only.
CREATE TABLE attachment (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  entity_type  text NOT NULL CHECK (entity_type IN ('person','organization','deal','activity','lead')),
  entity_id    uuid NOT NULL,
  filename     text NOT NULL,
  content_type text NULL,
  byte_size    bigint NULL,
  storage_key  text NOT NULL,      -- S3/MinIO object key
  checksum     text NULL,          -- sha256 for dedupe/integrity
  source       text NOT NULL,
  captured_by  text NOT NULL,
  created_at   timestamptz NOT NULL DEFAULT now(),
  updated_at   timestamptz NOT NULL DEFAULT now(),
  archived_at  timestamptz NULL
);
CREATE INDEX idx_attachment_entity ON attachment (workspace_id, entity_type, entity_id) WHERE archived_at IS NULL;
CREATE TRIGGER trg_attachment_updated BEFORE UPDATE ON attachment
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Manual per-record sharing (data-model §2.5, A52/ADR-0039): one flat,
-- audited grant table for all shareable types.
CREATE TABLE record_grant (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id  uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  record_type   text NOT NULL CHECK (record_type IN ('deal','person','organization','lead')),
  record_id     uuid NOT NULL,                          -- no cross-type FK; type-checked in app
  subject_type  text NOT NULL CHECK (subject_type IN ('user','team')),
  subject_id    uuid NOT NULL,                          -- app_user(id) or team(id) per subject_type
  access        text NOT NULL CHECK (access IN ('read','write')),  -- 'write' satisfies 'read'
  granted_by    uuid NOT NULL REFERENCES app_user(id) ON DELETE RESTRICT,
  reason        text NULL,
  expires_at    timestamptz NULL,                       -- an expired grant matches nothing
  created_at    timestamptz NOT NULL DEFAULT now(),
  version       bigint NOT NULL DEFAULT 1,
  CONSTRAINT record_grant_unique UNIQUE (workspace_id, record_type, record_id, subject_type, subject_id)
);
CREATE INDEX idx_record_grant_record  ON record_grant (workspace_id, record_type, record_id);
-- The spec's partial predicate (expires_at > now()) is not immutable and
-- cannot back an index; expiry is filtered in the visibility query instead.
CREATE INDEX idx_record_grant_subject ON record_grant (workspace_id, subject_type, subject_id);
