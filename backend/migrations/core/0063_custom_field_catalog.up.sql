-- 0063: custom_field — the catalog behind the governed runtime add-field
-- engine (decisions/0024, foundation tickets CF-T01-CF-T05). One row per
-- admin-defined scalar field; the catalog is the system-of-record for
-- every runtime cf_-prefixed column the engine later ADDs to a core
-- object's table (person/organization/deal/lead/activity) — column_name is
-- server-derived from label and never client-supplied
-- (CUSTOM-FIELDS-SCHEMA-2). No cf_ columns land here — the ALTER TABLE
-- engine runs on its own owner-privileged schema pool (decisions/0024) and
-- is a later ticket; this migration only ships the shape the engine will
-- write into, plus the unique indexes that turn a mid-transaction
-- slug/column collision into a whole-transaction rollback (including the
-- ALTER). No sidecar options table — picklist values live in the
-- `options` jsonb column (the catalog's own metadata, not the NEVER-1/
-- DM-CONV-16 EAV value store the engine is forbidden from creating).
--
-- DEVIATION (documented): no CHECK ties `currency` to `type = 'currency'`
-- — that conditional-required validation stays an application-layer
-- (Validate) concern rather than a non-trivial cross-column DDL expression.

CREATE TABLE custom_field (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id  uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  object        text NOT NULL CHECK (object IN ('person','organization','deal','lead','activity')),
  slug          text NOT NULL,
  label         text NOT NULL,
  type          text NOT NULL CHECK (type IN ('text','number','date','currency','picklist','boolean')),
  status        text NOT NULL DEFAULT 'active' CHECK (status IN ('active','retired')),
  column_name   text NOT NULL,
  currency      char(3) NULL CHECK (currency IS NULL OR currency ~ '^[A-Z]{3}$'),
  options       jsonb NULL,
  created_by    uuid NOT NULL,
  version       bigint NOT NULL DEFAULT 1,
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now(),
  archived_at   timestamptz NULL,

  -- Composite tenant FK (0019 pattern, TestFK_tenantLocalReferencesAreComposite):
  -- the creating user must live in the SAME workspace as the catalog row.
  CONSTRAINT custom_field_created_by_fkey FOREIGN KEY (workspace_id, created_by)
    REFERENCES app_user (workspace_id, id) ON DELETE RESTRICT
);

-- Per-workspace uniqueness, active or retired alike: a retired field's
-- slug/column_name stay reserved because the physical cf_ column (and any
-- data already in it) is never dropped by the lifecycle's retire path.
CREATE UNIQUE INDEX uq_custom_field_slug   ON custom_field (workspace_id, object, slug);
CREATE UNIQUE INDEX uq_custom_field_column ON custom_field (workspace_id, object, column_name);
-- Supports the catalog list query: object is required, status optionally narrows.
CREATE INDEX idx_custom_field_object ON custom_field (workspace_id, object, status);

CREATE TRIGGER trg_custom_field_updated BEFORE UPDATE ON custom_field
  FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();

ALTER TABLE custom_field ENABLE ROW LEVEL SECURITY;
ALTER TABLE custom_field FORCE ROW LEVEL SECURITY;
CREATE POLICY custom_field_tenant_isolation ON custom_field
  USING      (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);

