-- Reverses 0127. The vocabulary CHECKs narrow back first: they must stop
-- admitting 'project' before the rows referencing projects go away.

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
