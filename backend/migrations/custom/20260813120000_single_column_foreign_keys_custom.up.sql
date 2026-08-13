-- ADR-0091 §8 phase C, custom half: core 0218 rewrites the foreign keys that
-- exist when IT runs, and the custom namespace's tables do not yet. Same
-- transformation, same provenance — read out of pg_get_constraintdef.

ALTER TABLE import_record_map DROP CONSTRAINT import_record_map_import_run_id_fkey;
ALTER TABLE import_record_map ADD CONSTRAINT import_record_map_import_run_id_fkey FOREIGN KEY (import_run_id) REFERENCES import_run(id) ON DELETE RESTRICT;

ALTER TABLE mirror_user_automap_block DROP CONSTRAINT mirror_user_automap_block_app_user_id_fkey;
ALTER TABLE mirror_user_automap_block ADD CONSTRAINT mirror_user_automap_block_app_user_id_fkey FOREIGN KEY (app_user_id) REFERENCES app_user(id) ON DELETE CASCADE;

ALTER TABLE mirror_user_automap_block DROP CONSTRAINT mirror_user_automap_block_blocked_by_fkey;
ALTER TABLE mirror_user_automap_block ADD CONSTRAINT mirror_user_automap_block_blocked_by_fkey FOREIGN KEY (blocked_by) REFERENCES app_user(id) ON DELETE RESTRICT;

ALTER TABLE mirror_user_map DROP CONSTRAINT mirror_user_map_app_user_id_fkey;
ALTER TABLE mirror_user_map ADD CONSTRAINT mirror_user_map_app_user_id_fkey FOREIGN KEY (app_user_id) REFERENCES app_user(id) ON DELETE CASCADE;
