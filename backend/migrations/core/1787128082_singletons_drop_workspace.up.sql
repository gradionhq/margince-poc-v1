-- 1787128082: the last per-record singletons drop the tenant column
-- (ADR-0091 §8 phase D).
--
-- Seven tables, each a sidecar on something that already lost its own tenant:
--
--   field_provenance       where one field's current value came from
--   idempotency_key        a replayed request's stored answer
--   linkedin_account       a human's connected LinkedIn identity
--   linkedin_connection    one imported connection of theirs, pre-match
--   signal_thread_scan     how far the signal reader got through a thread
--   suggestion_dismissal   a suggestion this reader has waved away
--   user_record_view       when a reader last opened a record
--
-- Every one hangs off a user or a record whose own tenant column is already
-- gone, so the foreign key carries what this column was restating. None has a
-- uniqueness that names the tenant and none carries a phase-B leftover: the
-- only thing the column still held was its foreign key to workspace.

ALTER TABLE user_record_view DROP CONSTRAINT user_record_view_workspace_id_fkey;
ALTER TABLE user_record_view DROP COLUMN workspace_id;

ALTER TABLE suggestion_dismissal DROP CONSTRAINT suggestion_dismissal_workspace_id_fkey;
ALTER TABLE suggestion_dismissal DROP COLUMN workspace_id;

ALTER TABLE signal_thread_scan DROP CONSTRAINT signal_thread_scan_workspace_id_fkey;
ALTER TABLE signal_thread_scan DROP COLUMN workspace_id;

ALTER TABLE linkedin_connection DROP CONSTRAINT linkedin_connection_workspace_id_fkey;
ALTER TABLE linkedin_connection DROP COLUMN workspace_id;

ALTER TABLE linkedin_account DROP CONSTRAINT linkedin_account_workspace_id_fkey;
ALTER TABLE linkedin_account DROP COLUMN workspace_id;

ALTER TABLE idempotency_key DROP CONSTRAINT idempotency_key_workspace_id_fkey;
ALTER TABLE idempotency_key DROP COLUMN workspace_id;

ALTER TABLE field_provenance DROP CONSTRAINT field_provenance_workspace_id_fkey;
ALTER TABLE field_provenance DROP COLUMN workspace_id;

-- The indexes that led with the column, recreated on what actually selects
-- rows: the field a provenance row is about, the address or name a connection
-- is matched by, the account a dismissal is against. Every predicate carries
-- over unchanged.
CREATE INDEX idx_field_provenance_object ON field_provenance
  (object_type, object_id, field_name, captured_at DESC);

CREATE INDEX idx_linkedin_connection_email ON linkedin_connection (lower(email))
  WHERE email IS NOT NULL AND tombstoned_at IS NULL;
CREATE INDEX idx_linkedin_connection_match ON linkedin_connection
  (normalized_name, normalized_company) WHERE tombstoned_at IS NULL;
CREATE INDEX idx_linkedin_connection_org ON linkedin_connection (matched_org_id)
  WHERE matched_org_id IS NOT NULL AND tombstoned_at IS NULL;

CREATE INDEX suggestion_dismissal_organization_ix ON suggestion_dismissal (organization_id);
