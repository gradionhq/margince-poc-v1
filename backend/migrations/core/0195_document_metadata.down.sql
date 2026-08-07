-- Reverses 0195. The metadata is dropped with its constraints and indexes; the
-- files themselves and their primary parents are untouched, because this
-- migration only ever added a description of them.
DROP INDEX IF EXISTS attachment_account_ix;
DROP INDEX IF EXISTS attachment_external_part_key;
ALTER TABLE attachment DROP CONSTRAINT IF EXISTS attachment_supersedes_fkey;
ALTER TABLE attachment DROP CONSTRAINT IF EXISTS uq_attachment_ws_id;
ALTER TABLE attachment DROP CONSTRAINT IF EXISTS attachment_supersedes_not_self;
ALTER TABLE attachment
  DROP COLUMN IF EXISTS category,
  DROP COLUMN IF EXISTS title,
  DROP COLUMN IF EXISTS doc_state,
  DROP COLUMN IF EXISTS pinned,
  DROP COLUMN IF EXISTS supersedes_id,
  DROP COLUMN IF EXISTS organization_id,
  DROP COLUMN IF EXISTS deal_id,
  DROP COLUMN IF EXISTS project_id,
  DROP COLUMN IF EXISTS activity_id,
  DROP COLUMN IF EXISTS external_source_id,
  DROP COLUMN IF EXISTS external_part_id,
  DROP COLUMN IF EXISTS declared_type;
