-- 20260721130000_overlay_purge_indexes: index the two lookups a per-record
-- purge (overlay/mirrordeletion.go's PurgeRecord, and the visibility
-- clear/recompute in overlay/visibility.go) makes that the existing primary
-- keys do NOT already prefix-cover, so purging one incumbent-deleted record
-- does not scan the whole workspace's association / visibility rows.
--
-- overlay_association PK is (workspace_id, from_type, from_id, to_type,
-- to_id, type_id): the from-side delete predicate rides that PK prefix
-- already, but the to-side (to_type, to_id) predicate does not — index it.
CREATE INDEX IF NOT EXISTS idx_overlay_association_to
  ON overlay_association (workspace_id, to_type, to_id);

-- mirror_visibility PK is (workspace_id, incumbent, mirror_user_id,
-- object_class, external_id): a delete/clear keyed on (object_class,
-- external_id) — the per-record purge and ProjectOwnerVisibility's
-- clear-then-grant — has no usable PK prefix, so index the record key.
CREATE INDEX IF NOT EXISTS idx_mirror_visibility_record
  ON mirror_visibility (workspace_id, object_class, external_id);
