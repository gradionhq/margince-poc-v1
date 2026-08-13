-- Reverse of 0228: the six tables carry the tenant column again.
--
-- The column returns nullable, is filled from the installation's single
-- workspace, and only then becomes NOT NULL — the shape 0227 established.
-- If `workspace` is empty and a table is not, SET NOT NULL fails and the
-- rollback stops, which is the honest outcome: the rows belonged to a
-- workspace that no longer exists.

ALTER TABLE list ADD COLUMN workspace_id uuid;
ALTER TABLE list_member ADD COLUMN workspace_id uuid;
ALTER TABLE tag ADD COLUMN workspace_id uuid;
ALTER TABLE taggable ADD COLUMN workspace_id uuid;
ALTER TABLE saved_view ADD COLUMN workspace_id uuid;
ALTER TABLE quota ADD COLUMN workspace_id uuid;

DO $$
DECLARE ws uuid := (SELECT id FROM workspace ORDER BY created_at LIMIT 1);
BEGIN
  UPDATE list SET workspace_id = ws;
  UPDATE list_member SET workspace_id = ws;
  UPDATE tag SET workspace_id = ws;
  UPDATE taggable SET workspace_id = ws;
  UPDATE saved_view SET workspace_id = ws;
  UPDATE quota SET workspace_id = ws;
END $$;

ALTER TABLE list ALTER COLUMN workspace_id SET NOT NULL;
ALTER TABLE list_member ALTER COLUMN workspace_id SET NOT NULL;
ALTER TABLE tag ALTER COLUMN workspace_id SET NOT NULL;
ALTER TABLE taggable ALTER COLUMN workspace_id SET NOT NULL;
ALTER TABLE saved_view ALTER COLUMN workspace_id SET NOT NULL;
ALTER TABLE quota ALTER COLUMN workspace_id SET NOT NULL;

ALTER TABLE list ADD CONSTRAINT list_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE list_member ADD CONSTRAINT list_member_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE tag ADD CONSTRAINT tag_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE taggable ADD CONSTRAINT taggable_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE saved_view ADD CONSTRAINT saved_view_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE quota ADD CONSTRAINT quota_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;

ALTER TABLE list ADD CONSTRAINT uq_list_ws_id UNIQUE (id);
ALTER TABLE tag ADD CONSTRAINT uq_tag_ws_id UNIQUE (id);

DROP INDEX idx_list_member_entity;
CREATE INDEX idx_list_member_entity ON list_member (workspace_id, entity_type, entity_id);

DROP INDEX idx_taggable_entity;
CREATE INDEX idx_taggable_entity ON taggable (workspace_id, entity_type, entity_id);

DROP INDEX idx_saved_view_owner;
CREATE INDEX idx_saved_view_owner ON saved_view (workspace_id, owner_id, resource) WHERE archived_at IS NULL;

DROP INDEX idx_quota_owner;
CREATE INDEX idx_quota_owner ON quota (workspace_id, owner_id) WHERE owner_id IS NOT NULL;

DROP INDEX idx_quota_team;
CREATE INDEX idx_quota_team ON quota (workspace_id, team_id) WHERE team_id IS NOT NULL;

CREATE INDEX idx_quota_ws_live ON quota (workspace_id) WHERE archived_at IS NULL;
