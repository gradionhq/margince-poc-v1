-- AMENDED (ADR-0091 §8 phase D): the composite foreign keys below referenced
-- app_user (workspace_id, id) and now reference app_user (id). `dbmigrate.Up`
-- applies ALL of `core` before ANY of `custom`, so on a FRESH database this
-- file runs against the FINAL core schema — where app_user has no workspace_id
-- and no UNIQUE (workspace_id, id) for a composite key to point at — and the
-- install fails outright. Amending is what keeps a fresh install working.
--
-- The tenancy integrity the composite form bought is not lost, it has no
-- subject: a single-organization installation (ADR-0061) has one workspace, so
-- there is no cross-workspace target for the database to reject. This is the
-- same rewrite 20260813120000_single_column_foreign_keys_custom already applies
-- to these constraints at a later version; a deployed database reaches the same
-- shape either way.
-- Reverse of the custom half of phase C.

ALTER TABLE mirror_user_map DROP CONSTRAINT mirror_user_map_app_user_id_fkey;
ALTER TABLE mirror_user_map ADD CONSTRAINT mirror_user_map_app_user_id_fkey FOREIGN KEY (app_user_id)
REFERENCES app_user (id) ON DELETE CASCADE;

ALTER TABLE mirror_user_automap_block DROP CONSTRAINT mirror_user_automap_block_blocked_by_fkey;
ALTER TABLE mirror_user_automap_block ADD CONSTRAINT mirror_user_automap_block_blocked_by_fkey FOREIGN KEY (blocked_by)
REFERENCES app_user (id) ON DELETE RESTRICT;

ALTER TABLE mirror_user_automap_block DROP CONSTRAINT mirror_user_automap_block_app_user_id_fkey;
ALTER TABLE mirror_user_automap_block ADD CONSTRAINT mirror_user_automap_block_app_user_id_fkey FOREIGN KEY (app_user_id)
REFERENCES app_user (id) ON DELETE CASCADE;

ALTER TABLE import_record_map DROP CONSTRAINT import_record_map_import_run_id_fkey;
ALTER TABLE import_record_map ADD CONSTRAINT import_record_map_import_run_id_fkey FOREIGN KEY (workspace_id, import_run_id) REFERENCES import_run(workspace_id, id) ON DELETE RESTRICT;
