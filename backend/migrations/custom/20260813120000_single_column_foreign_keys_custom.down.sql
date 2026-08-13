-- Reverse of the custom half of phase C.

ALTER TABLE mirror_user_map DROP CONSTRAINT mirror_user_map_app_user_id_fkey;
ALTER TABLE mirror_user_map ADD CONSTRAINT mirror_user_map_app_user_id_fkey FOREIGN KEY (workspace_id, app_user_id) REFERENCES app_user(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE mirror_user_automap_block DROP CONSTRAINT mirror_user_automap_block_blocked_by_fkey;
ALTER TABLE mirror_user_automap_block ADD CONSTRAINT mirror_user_automap_block_blocked_by_fkey FOREIGN KEY (workspace_id, blocked_by) REFERENCES app_user(workspace_id, id) ON DELETE RESTRICT;

ALTER TABLE mirror_user_automap_block DROP CONSTRAINT mirror_user_automap_block_app_user_id_fkey;
ALTER TABLE mirror_user_automap_block ADD CONSTRAINT mirror_user_automap_block_app_user_id_fkey FOREIGN KEY (workspace_id, app_user_id) REFERENCES app_user(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE import_record_map DROP CONSTRAINT import_record_map_import_run_id_fkey;
ALTER TABLE import_record_map ADD CONSTRAINT import_record_map_import_run_id_fkey FOREIGN KEY (workspace_id, import_run_id) REFERENCES import_run(workspace_id, id) ON DELETE RESTRICT;
