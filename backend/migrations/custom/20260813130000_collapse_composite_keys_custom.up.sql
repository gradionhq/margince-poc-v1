-- AMENDED (ADR-0091 §8 phase D): the app_user arm of this collapse is removed.
-- `dbmigrate.Up` applies ALL of `core` before ANY of `custom`, so on a FRESH
-- database this file runs against the FINAL core schema — where app_user has no
-- workspace_id, and uq_app_user_ws_id fell with the column — and DROP
-- CONSTRAINT fails, taking the install with it. A deployed database already ran
-- the original and reached UNIQUE (id); the core drop leaves it at the same
-- shape, so both paths agree.
-- ADR-0091 §8 phase B, custom half — including the three tables keyed on the
-- tenant ALONE. Those are one-row-per-workspace tables: one incumbent
-- connection, one mirror halt, one sync state. Dropping the key would not
-- collapse them, it would let each hold many rows where it held one, so the
-- key becomes a constant expression instead.
--
-- `app_user` is here for a different reason: core cannot drop its composite
-- unique, because a historical migration IN THIS NAMESPACE still creates a
-- foreign key against it. The drop has to follow the chain that depends on it.

ALTER TABLE import_record_map DROP CONSTRAINT import_record_map_pkey;
ALTER TABLE import_record_map ADD CONSTRAINT import_record_map_pkey PRIMARY KEY (source_system, object, external_id);

ALTER TABLE import_run DROP CONSTRAINT uq_import_run_workspace_id;
ALTER TABLE import_run ADD CONSTRAINT uq_import_run_workspace_id UNIQUE (id);

ALTER TABLE mirror_user_automap_block DROP CONSTRAINT mirror_user_automap_block_pkey;
ALTER TABLE mirror_user_automap_block ADD CONSTRAINT mirror_user_automap_block_pkey PRIMARY KEY (app_user_id, incumbent);

ALTER TABLE mirror_user_map DROP CONSTRAINT mirror_user_map_workspace_id_app_user_id_incumbent_key;
ALTER TABLE mirror_user_map ADD CONSTRAINT mirror_user_map_workspace_id_app_user_id_incumbent_key UNIQUE (app_user_id, incumbent);

ALTER TABLE mirror_visibility DROP CONSTRAINT mirror_visibility_pkey;
ALTER TABLE mirror_visibility ADD CONSTRAINT mirror_visibility_pkey PRIMARY KEY (incumbent, mirror_user_id, object_class, external_id);

ALTER TABLE overlay_association DROP CONSTRAINT overlay_association_pkey;
ALTER TABLE overlay_association ADD CONSTRAINT overlay_association_pkey PRIMARY KEY (from_type, from_id, to_type, to_id, type_id);

ALTER TABLE overlay_backfill_cursor DROP CONSTRAINT overlay_backfill_cursor_pkey;
ALTER TABLE overlay_backfill_cursor ADD CONSTRAINT overlay_backfill_cursor_pkey PRIMARY KEY (object_class);

ALTER TABLE overlay_mirror DROP CONSTRAINT overlay_mirror_pkey;
ALTER TABLE overlay_mirror ADD CONSTRAINT overlay_mirror_pkey PRIMARY KEY (object_class, external_id);

ALTER TABLE overlay_reconcile_watermark DROP CONSTRAINT overlay_reconcile_watermark_pkey;
ALTER TABLE overlay_reconcile_watermark ADD CONSTRAINT overlay_reconcile_watermark_pkey PRIMARY KEY (object_class);

ALTER TABLE overlay_tombstone DROP CONSTRAINT overlay_tombstone_pkey;
ALTER TABLE overlay_tombstone ADD CONSTRAINT overlay_tombstone_pkey PRIMARY KEY (object_class, external_id);

ALTER TABLE overlay_write_ledger DROP CONSTRAINT overlay_write_ledger_pkey;
ALTER TABLE overlay_write_ledger ADD CONSTRAINT overlay_write_ledger_pkey PRIMARY KEY (object_class, external_id, property, value_hash);

ALTER TABLE incumbent_connection DROP CONSTRAINT incumbent_connection_workspace_id_key;
CREATE UNIQUE INDEX incumbent_connection_singleton ON incumbent_connection ((true));

ALTER TABLE overlay_mirror_halt DROP CONSTRAINT overlay_mirror_halt_pkey;
CREATE UNIQUE INDEX overlay_mirror_halt_singleton ON overlay_mirror_halt ((true));

ALTER TABLE overlay_sync_state DROP CONSTRAINT overlay_sync_state_pkey;
CREATE UNIQUE INDEX overlay_sync_state_singleton ON overlay_sync_state ((true));
ALTER INDEX mirror_user_map_workspace_id_app_user_id_incumbent_key RENAME TO mirror_user_map_app_user_id_incumbent_key;
ALTER INDEX uq_import_run_workspace_id RENAME TO uq_import_run;
