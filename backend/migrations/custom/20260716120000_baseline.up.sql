-- SPDX-License-Identifier: BUSL-1.1
-- SPDX-FileCopyrightText: 2026 Gradion

-- What custom adds on top of core: the overlay mirror substrate, the import
-- ledger, and the columns the fork seam puts on core's own workspace table.
--
-- THE ORDER IS A DEPENDENCY ORDER:
--
--   1. the x_ columns on workspace (ADR-0017's fork seam). Every one is
--      x_-prefixed and lives on a table CORE owns, which is exactly what the
--      prefix is for: a core upgrade that adds a column to workspace cannot
--      collide with one of these, so the two stay independently upgradeable.
--      IF NOT EXISTS throughout, because this lands on a database core has
--      already built and must not care whether an earlier attempt got halfway
--   2. custom's own tables
--   3. their constraints, indexes and the grants that let the application role
--      read and write them
--
-- Applied AFTER core, always — custom is a separate ledger with its own
-- tracking table, and dbmigrate fixes the order (ADR-0017).

ALTER TABLE workspace ADD COLUMN IF NOT EXISTS x_sor_mode text NOT NULL DEFAULT 'native';
ALTER TABLE workspace ADD COLUMN IF NOT EXISTS x_incumbent text;

ALTER TABLE workspace DROP CONSTRAINT IF EXISTS workspace_x_sor_mode_check;
ALTER TABLE workspace ADD CONSTRAINT workspace_x_sor_mode_check
  CHECK (x_sor_mode IN ('native','overlay'));

ALTER TABLE workspace DROP CONSTRAINT IF EXISTS workspace_x_incumbent_check;
ALTER TABLE workspace ADD CONSTRAINT workspace_x_incumbent_check
  CHECK (x_incumbent IS NULL OR x_incumbent IN ('hubspot','salesforce','dynamics'));

-- The two columns are meaningful only together: overlay mode names an incumbent,
-- native mode names none. A row satisfying both checks above and neither half of
-- this one is a workspace mirroring nothing, or mirroring a system it is not in
-- overlay mode for.
ALTER TABLE workspace DROP CONSTRAINT IF EXISTS x_overlay_iff_incumbent;
ALTER TABLE workspace ADD CONSTRAINT x_overlay_iff_incumbent
  CHECK ((x_sor_mode = 'overlay') = (x_incumbent IS NOT NULL));

CREATE TABLE import_record_map (
    source_system text NOT NULL,
    object text NOT NULL,
    external_id text NOT NULL,
    native_id uuid NOT NULL,
    import_run_id uuid NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL
);

CREATE TABLE import_run (
    id uuid DEFAULT uuidv7() NOT NULL,
    connector text NOT NULL,
    status text DEFAULT 'pending'::text NOT NULL,
    mapping jsonb DEFAULT '{}'::jsonb NOT NULL,
    source_ref text NOT NULL,
    report jsonb,
    error text,
    checkpoint integer DEFAULT 0 NOT NULL,
    source text NOT NULL,
    captured_by text NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    undo_report jsonb,
    CONSTRAINT import_run_connector_check CHECK (connector IN ('csv', 'hubspot', 'salesforce', 'bundle', 'mirror')),
    CONSTRAINT import_run_status_check CHECK (status IN ('pending', 'validating', 'awaiting_approval', 'running', 'complete', 'failed', 'undoing', 'undone'))
);

CREATE TABLE incumbent_connection (
    id uuid DEFAULT uuidv7() NOT NULL,
    incumbent text NOT NULL,
    region text NOT NULL,
    credential_ref text NOT NULL,
    scopes text[] DEFAULT '{}'::text[] NOT NULL,
    status text DEFAULT 'active'::text NOT NULL,
    connected_at timestamptz DEFAULT now() NOT NULL,
    revoked_at timestamptz,
    incumbent_account_id text,
    CONSTRAINT incumbent_connection_status_check CHECK (status IN ('active', 'revoked', 'error'))
);

CREATE TABLE mirror_user_automap_block (
    app_user_id uuid NOT NULL,
    incumbent text NOT NULL,
    blocked_at timestamptz DEFAULT now() NOT NULL,
    blocked_by uuid NOT NULL
);

CREATE TABLE mirror_user_map (
    app_user_id uuid NOT NULL,
    incumbent text NOT NULL,
    incumbent_user_id text NOT NULL,
    match_source text DEFAULT 'email'::text NOT NULL,
    CONSTRAINT mirror_user_map_match_source_check CHECK (match_source IN ('email', 'manual'))
);

CREATE TABLE mirror_visibility (
    incumbent text NOT NULL,
    mirror_user_id uuid NOT NULL,
    object_class text NOT NULL,
    external_id text NOT NULL,
    can_see boolean NOT NULL,
    snapshot_at timestamptz DEFAULT now() NOT NULL
);

CREATE TABLE overlay_association (
    from_type text NOT NULL,
    from_id text NOT NULL,
    to_type text NOT NULL,
    to_id text NOT NULL,
    type_id integer NOT NULL,
    category text NOT NULL,
    label text,
    direction text NOT NULL
);

CREATE TABLE overlay_backfill_cursor (
    object_class text NOT NULL,
    cursor text DEFAULT ''::text NOT NULL,
    done boolean DEFAULT false NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    truncated boolean DEFAULT false NOT NULL
);

CREATE TABLE overlay_mirror (
    object_class text NOT NULL,
    external_id text NOT NULL,
    fields jsonb NOT NULL,
    updated_at_baseline timestamptz NOT NULL,
    sync_state text DEFAULT 'fresh'::text NOT NULL,
    last_synced_at timestamptz DEFAULT now() NOT NULL,
    owner_external_id text,
    projection_fingerprint text,
    reprojection_failed_for text,
    CONSTRAINT overlay_mirror_sync_state_check CHECK (sync_state IN ('fresh', 'pending_sync', 'stale'))
);

CREATE TABLE overlay_mirror_halt (
    reason text NOT NULL,
    detected_at timestamptz DEFAULT now() NOT NULL
);

CREATE TABLE overlay_reconcile_watermark (
    object_class text NOT NULL,
    watermark timestamptz NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL
);

CREATE TABLE overlay_sync_state (
    next_sweep_at timestamptz DEFAULT now() NOT NULL,
    consecutive_failures integer DEFAULT 0 NOT NULL,
    last_error_class text,
    last_success_at timestamptz,
    last_failure_at timestamptz,
    updated_at timestamptz DEFAULT now() NOT NULL,
    flip_snapshot_id text,
    mirror_frozen_at timestamptz,
    CONSTRAINT overlay_sync_state_last_error_class_check CHECK (last_error_class IS NULL OR last_error_class IN ('rate_limited', 'auth', 'internal'))
);

CREATE TABLE overlay_tombstone (
    object_class text NOT NULL,
    external_id text NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL
);

CREATE TABLE overlay_write_ledger (
    object_class text NOT NULL,
    external_id text NOT NULL,
    property text NOT NULL,
    value_hash text NOT NULL,
    opened_at timestamptz DEFAULT now() NOT NULL,
    value_canonical text DEFAULT ''::text NOT NULL
);

ALTER TABLE import_record_map
    ADD CONSTRAINT import_record_map_pkey PRIMARY KEY (source_system, object, external_id);

ALTER TABLE import_run
    ADD CONSTRAINT import_run_pkey PRIMARY KEY (id);

ALTER TABLE incumbent_connection
    ADD CONSTRAINT incumbent_connection_pkey PRIMARY KEY (id);

ALTER TABLE mirror_user_automap_block
    ADD CONSTRAINT mirror_user_automap_block_pkey PRIMARY KEY (app_user_id, incumbent);

ALTER TABLE mirror_user_map
    ADD CONSTRAINT mirror_user_map_app_user_id_incumbent_key UNIQUE (app_user_id, incumbent);

ALTER TABLE mirror_visibility
    ADD CONSTRAINT mirror_visibility_pkey PRIMARY KEY (incumbent, mirror_user_id, object_class, external_id);

ALTER TABLE overlay_association
    ADD CONSTRAINT overlay_association_pkey PRIMARY KEY (from_type, from_id, to_type, to_id, type_id);

ALTER TABLE overlay_backfill_cursor
    ADD CONSTRAINT overlay_backfill_cursor_pkey PRIMARY KEY (object_class);

ALTER TABLE overlay_mirror
    ADD CONSTRAINT overlay_mirror_pkey PRIMARY KEY (object_class, external_id);

ALTER TABLE overlay_reconcile_watermark
    ADD CONSTRAINT overlay_reconcile_watermark_pkey PRIMARY KEY (object_class);

ALTER TABLE overlay_tombstone
    ADD CONSTRAINT overlay_tombstone_pkey PRIMARY KEY (object_class, external_id);

ALTER TABLE overlay_write_ledger
    ADD CONSTRAINT overlay_write_ledger_pkey PRIMARY KEY (object_class, external_id, property, value_hash);

ALTER TABLE import_run
    ADD CONSTRAINT uq_import_run UNIQUE (id);

CREATE INDEX idx_import_record_map_run ON import_record_map USING btree (import_run_id);

CREATE INDEX idx_import_run_ws ON import_run USING btree (status);

CREATE INDEX idx_incumbent_connection_account ON incumbent_connection USING btree (incumbent, incumbent_account_id) WHERE ((status = 'active'::text) AND (incumbent_account_id IS NOT NULL));

CREATE INDEX idx_mirror_user_map ON mirror_user_map USING btree (incumbent, incumbent_user_id);

CREATE INDEX idx_mirror_visibility_record ON mirror_visibility USING btree (object_class, external_id);

CREATE INDEX idx_overlay_association_to ON overlay_association USING btree (to_type, to_id);

CREATE INDEX idx_overlay_sync_due ON overlay_sync_state USING btree (next_sweep_at);

CREATE UNIQUE INDEX incumbent_connection_singleton ON incumbent_connection USING btree ((true));

CREATE UNIQUE INDEX overlay_mirror_halt_singleton ON overlay_mirror_halt USING btree ((true));

CREATE UNIQUE INDEX overlay_sync_state_singleton ON overlay_sync_state USING btree ((true));

ALTER TABLE import_record_map
    ADD CONSTRAINT import_record_map_import_run_id_fkey FOREIGN KEY (import_run_id) REFERENCES import_run(id) ON DELETE RESTRICT;

ALTER TABLE mirror_user_automap_block
    ADD CONSTRAINT mirror_user_automap_block_app_user_id_fkey FOREIGN KEY (app_user_id) REFERENCES app_user(id) ON DELETE CASCADE;

ALTER TABLE mirror_user_automap_block
    ADD CONSTRAINT mirror_user_automap_block_blocked_by_fkey FOREIGN KEY (blocked_by) REFERENCES app_user(id) ON DELETE RESTRICT;

ALTER TABLE mirror_user_map
    ADD CONSTRAINT mirror_user_map_app_user_id_fkey FOREIGN KEY (app_user_id) REFERENCES app_user(id) ON DELETE CASCADE;

GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE import_record_map TO margince_app;

GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE import_run TO margince_app;

GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE incumbent_connection TO margince_app;

GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE mirror_user_automap_block TO margince_app;

GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE mirror_user_map TO margince_app;

GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE mirror_visibility TO margince_app;

GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE overlay_association TO margince_app;

GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE overlay_backfill_cursor TO margince_app;

GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE overlay_mirror TO margince_app;

GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE overlay_mirror_halt TO margince_app;

GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE overlay_reconcile_watermark TO margince_app;

GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE overlay_sync_state TO margince_app;

GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE overlay_tombstone TO margince_app;

GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE overlay_write_ledger TO margince_app;

