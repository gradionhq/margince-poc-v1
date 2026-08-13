-- Reverse of the custom half of phase B.

ALTER INDEX uq_import_run RENAME TO uq_import_run_workspace_id;
ALTER INDEX mirror_user_map_app_user_id_incumbent_key RENAME TO mirror_user_map_workspace_id_app_user_id_incumbent_key;

DROP INDEX overlay_sync_state_singleton;
ALTER TABLE overlay_sync_state ADD CONSTRAINT overlay_sync_state_pkey PRIMARY KEY (workspace_id);

DROP INDEX overlay_mirror_halt_singleton;
ALTER TABLE overlay_mirror_halt ADD CONSTRAINT overlay_mirror_halt_pkey PRIMARY KEY (workspace_id);

DROP INDEX incumbent_connection_singleton;
ALTER TABLE incumbent_connection ADD CONSTRAINT incumbent_connection_workspace_id_key UNIQUE (workspace_id);

ALTER TABLE app_user DROP CONSTRAINT uq_app_user_ws_id;
ALTER TABLE app_user ADD CONSTRAINT uq_app_user_ws_id UNIQUE (workspace_id, id);

ALTER TABLE overlay_write_ledger DROP CONSTRAINT overlay_write_ledger_pkey;
ALTER TABLE overlay_write_ledger ADD CONSTRAINT overlay_write_ledger_pkey PRIMARY KEY (workspace_id, object_class, external_id, property, value_hash);

ALTER TABLE overlay_tombstone DROP CONSTRAINT overlay_tombstone_pkey;
ALTER TABLE overlay_tombstone ADD CONSTRAINT overlay_tombstone_pkey PRIMARY KEY (workspace_id, object_class, external_id);

ALTER TABLE overlay_reconcile_watermark DROP CONSTRAINT overlay_reconcile_watermark_pkey;
ALTER TABLE overlay_reconcile_watermark ADD CONSTRAINT overlay_reconcile_watermark_pkey PRIMARY KEY (workspace_id, object_class);

ALTER TABLE overlay_mirror DROP CONSTRAINT overlay_mirror_pkey;
ALTER TABLE overlay_mirror ADD CONSTRAINT overlay_mirror_pkey PRIMARY KEY (workspace_id, object_class, external_id);

ALTER TABLE overlay_backfill_cursor DROP CONSTRAINT overlay_backfill_cursor_pkey;
ALTER TABLE overlay_backfill_cursor ADD CONSTRAINT overlay_backfill_cursor_pkey PRIMARY KEY (workspace_id, object_class);

ALTER TABLE overlay_association DROP CONSTRAINT overlay_association_pkey;
ALTER TABLE overlay_association ADD CONSTRAINT overlay_association_pkey PRIMARY KEY (workspace_id, from_type, from_id, to_type, to_id, type_id);

ALTER TABLE mirror_visibility DROP CONSTRAINT mirror_visibility_pkey;
ALTER TABLE mirror_visibility ADD CONSTRAINT mirror_visibility_pkey PRIMARY KEY (workspace_id, incumbent, mirror_user_id, object_class, external_id);

ALTER TABLE mirror_user_map DROP CONSTRAINT mirror_user_map_workspace_id_app_user_id_incumbent_key;
ALTER TABLE mirror_user_map ADD CONSTRAINT mirror_user_map_workspace_id_app_user_id_incumbent_key UNIQUE (workspace_id, app_user_id, incumbent);

ALTER TABLE mirror_user_automap_block DROP CONSTRAINT mirror_user_automap_block_pkey;
ALTER TABLE mirror_user_automap_block ADD CONSTRAINT mirror_user_automap_block_pkey PRIMARY KEY (workspace_id, app_user_id, incumbent);

ALTER TABLE import_run DROP CONSTRAINT uq_import_run_workspace_id;
ALTER TABLE import_run ADD CONSTRAINT uq_import_run_workspace_id UNIQUE (workspace_id, id);

ALTER TABLE import_record_map DROP CONSTRAINT import_record_map_pkey;
ALTER TABLE import_record_map ADD CONSTRAINT import_record_map_pkey PRIMARY KEY (workspace_id, source_system, object, external_id);

