-- Reverses 0131. The project-typed ROWS go first, then the vocabularies
-- narrow back.
--
-- That order is forced: ADD CONSTRAINT ... CHECK validates the rows already
-- in the table, so narrowing a vocabulary while a 'project' row still holds
-- it aborts the whole rollback. On a fresh schema there is nothing to find
-- and either order appears to work, which is exactly why this has to be
-- stated rather than discovered on the one database that has data.
--
-- Deleting them is the honest reverse: the down migration drops the project
-- table itself further below, so a row pointing at a project cannot outlive
-- it either way.
DO $$
DECLARE ws uuid;
BEGIN
  FOR ws IN SELECT id FROM workspace LOOP
    PERFORM set_config('app.workspace_id', ws::text, true);
    DELETE FROM custom_field
    WHERE (object      = 'project')
      AND custom_field.workspace_id = ws;

    DELETE FROM field_provenance
    WHERE (object_type = 'project')
      AND field_provenance.workspace_id = ws;

    DELETE FROM embedding
    WHERE (entity_type = 'project')
      AND embedding.workspace_id = ws;

    DELETE FROM attachment
    WHERE (entity_type = 'project')
      AND attachment.workspace_id = ws;

    DELETE FROM record_grant
    WHERE (record_type = 'project')
      AND record_grant.workspace_id = ws;

    DELETE FROM taggable
    WHERE (entity_type = 'project')
      AND taggable.workspace_id = ws;

    DELETE FROM list_member
    WHERE (entity_type = 'project')
      AND list_member.workspace_id = ws;

    DELETE FROM list
    WHERE (entity_type = 'project')
      AND list.workspace_id = ws;

    DELETE FROM activity_link
    WHERE (entity_type = 'project')
      AND activity_link.workspace_id = ws;

    DELETE FROM relationship
    WHERE (kind        = 'project_stakeholder')
      AND relationship.workspace_id = ws;
  END LOOP;
END $$;

ALTER TABLE custom_field DROP CONSTRAINT custom_field_object_check;
ALTER TABLE custom_field ADD CONSTRAINT custom_field_object_check
  CHECK (object IN ('person','organization','deal','lead','activity'));

ALTER TABLE field_provenance DROP CONSTRAINT field_provenance_object_type_check;
ALTER TABLE field_provenance ADD CONSTRAINT field_provenance_object_type_check
  CHECK (object_type IN ('person','organization','deal','activity','lead'));

ALTER TABLE embedding DROP CONSTRAINT embedding_entity_type_check;
ALTER TABLE embedding ADD CONSTRAINT embedding_entity_type_check
  CHECK (entity_type IN ('person','organization','deal','lead','activity'));

ALTER TABLE attachment DROP CONSTRAINT attachment_entity_type_check;
ALTER TABLE attachment ADD CONSTRAINT attachment_entity_type_check
  CHECK (entity_type IN ('person','organization','deal','activity','lead'));

ALTER TABLE record_grant DROP CONSTRAINT record_grant_record_type_check;
ALTER TABLE record_grant ADD CONSTRAINT record_grant_record_type_check
  CHECK (record_type IN ('deal','person','organization','lead'));

ALTER TABLE taggable DROP CONSTRAINT taggable_entity_type_check;
ALTER TABLE taggable ADD CONSTRAINT taggable_entity_type_check
  CHECK (entity_type IN ('person','organization','deal','lead'));

ALTER TABLE list_member DROP CONSTRAINT list_member_entity_type_check;
ALTER TABLE list_member ADD CONSTRAINT list_member_entity_type_check
  CHECK (entity_type IN ('person','organization','deal','lead'));

ALTER TABLE list DROP CONSTRAINT list_entity_type_check;
ALTER TABLE list ADD CONSTRAINT list_entity_type_check
  CHECK (entity_type IN ('person','organization','deal','lead'));

DROP INDEX uq_rel_project_stakeholder;
DROP INDEX idx_rel_person_projects;
DROP INDEX idx_rel_project_stakeholders;

ALTER TABLE relationship DROP CONSTRAINT rel_partner_shape;
ALTER TABLE relationship ADD CONSTRAINT rel_partner_shape CHECK (
  kind NOT IN ('partner_of','referred_by','co_sell_with')
  OR (organization_id IS NOT NULL AND counterparty_org_id IS NOT NULL
      AND organization_id <> counterparty_org_id AND person_id IS NULL AND deal_id IS NULL)
);
ALTER TABLE relationship DROP CONSTRAINT rel_stakeholder_shape;
ALTER TABLE relationship ADD CONSTRAINT rel_stakeholder_shape CHECK (
  kind <> 'deal_stakeholder' OR (deal_id IS NOT NULL AND person_id IS NOT NULL AND organization_id IS NULL)
);
ALTER TABLE relationship DROP CONSTRAINT rel_employment_shape;
ALTER TABLE relationship ADD CONSTRAINT rel_employment_shape CHECK (
  kind <> 'employment' OR (person_id IS NOT NULL AND organization_id IS NOT NULL AND deal_id IS NULL)
);
ALTER TABLE relationship DROP CONSTRAINT rel_project_stakeholder_shape;
ALTER TABLE relationship DROP CONSTRAINT relationship_kind_check;
ALTER TABLE relationship ADD CONSTRAINT relationship_kind_check
  CHECK (kind IN ('employment','deal_stakeholder','partner_of','referred_by','co_sell_with'));
ALTER TABLE relationship DROP CONSTRAINT relationship_project_id_fkey;
ALTER TABLE relationship DROP COLUMN project_id;

DROP TRIGGER trg_deal_project_same_org ON deal;
DROP FUNCTION assert_deal_project_same_org();
DROP INDEX idx_deal_project;
ALTER TABLE deal DROP CONSTRAINT deal_project_id_fkey;
ALTER TABLE deal DROP COLUMN project_id;

DROP INDEX uq_activity_link_project;
DROP INDEX idx_alink_project;
DROP INDEX uq_activity_link;
CREATE UNIQUE INDEX uq_activity_link
  ON activity_link (activity_id, entity_type, coalesce(person_id, organization_id, deal_id, lead_id));

ALTER TABLE activity_link DROP CONSTRAINT activity_link_shape;
ALTER TABLE activity_link ADD CONSTRAINT activity_link_shape CHECK (
  (entity_type='person'       AND person_id IS NOT NULL AND organization_id IS NULL AND deal_id IS NULL AND lead_id IS NULL) OR
  (entity_type='organization' AND organization_id IS NOT NULL AND person_id IS NULL AND deal_id IS NULL AND lead_id IS NULL) OR
  (entity_type='deal'         AND deal_id IS NOT NULL AND person_id IS NULL AND organization_id IS NULL AND lead_id IS NULL) OR
  (entity_type='lead'         AND lead_id IS NOT NULL AND person_id IS NULL AND organization_id IS NULL AND deal_id IS NULL)
);
ALTER TABLE activity_link DROP CONSTRAINT activity_link_entity_type_check;
ALTER TABLE activity_link ADD CONSTRAINT activity_link_entity_type_check
  CHECK (entity_type IN ('person','organization','deal','lead'));
ALTER TABLE activity_link DROP CONSTRAINT activity_link_project_id_fkey;
ALTER TABLE activity_link DROP COLUMN project_id;

DROP TABLE project_phase_history;
DROP TABLE project;
