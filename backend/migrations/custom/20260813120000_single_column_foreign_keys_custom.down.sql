-- AMENDED (ADR-0091 §8 phase D): the composite foreign keys this down half
-- restored referenced app_user (workspace_id, id) and now reference
-- app_user (id). A down half never runs on a fresh install, so the reason is
-- the ROLLBACK path: app_user has no workspace_id and no UNIQUE
-- (workspace_id, id) for a composite key to point at, so restoring the
-- composite form would fail.
--
-- What this costs, stated rather than left to be discovered: rolling this
-- migration back no longer restores the composite constraints its up half
-- replaced. It re-adds the single-column ones, so the rollback is a no-op on
-- shape. The tenancy integrity the composite form bought has no subject
-- anyway — a single-organization installation (ADR-0061) has one workspace, so
-- there is no cross-workspace target for the database to reject.
-- Reverse of the custom half of phase C.

ALTER TABLE mirror_user_map DROP CONSTRAINT mirror_user_map_app_user_id_fkey;
ALTER TABLE mirror_user_map ADD CONSTRAINT mirror_user_map_app_user_id_fkey FOREIGN KEY (app_user_id) REFERENCES app_user (id) ON DELETE CASCADE;

ALTER TABLE mirror_user_automap_block DROP CONSTRAINT mirror_user_automap_block_blocked_by_fkey;
ALTER TABLE mirror_user_automap_block ADD CONSTRAINT mirror_user_automap_block_blocked_by_fkey FOREIGN KEY (blocked_by) REFERENCES app_user (id) ON DELETE RESTRICT;

ALTER TABLE mirror_user_automap_block DROP CONSTRAINT mirror_user_automap_block_app_user_id_fkey;
ALTER TABLE mirror_user_automap_block ADD CONSTRAINT mirror_user_automap_block_app_user_id_fkey FOREIGN KEY (app_user_id) REFERENCES app_user (id) ON DELETE CASCADE;

ALTER TABLE import_record_map DROP CONSTRAINT import_record_map_import_run_id_fkey;
ALTER TABLE import_record_map ADD CONSTRAINT import_record_map_import_run_id_fkey FOREIGN KEY (workspace_id, import_run_id) REFERENCES import_run(workspace_id, id) ON DELETE RESTRICT;
