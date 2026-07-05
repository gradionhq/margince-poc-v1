DROP INDEX idx_alink_lead;
DROP INDEX uq_activity_link;
CREATE UNIQUE INDEX uq_activity_link
  ON activity_link (activity_id, entity_type, coalesce(person_id, organization_id, deal_id));
ALTER TABLE activity_link DROP CONSTRAINT activity_link_shape;
ALTER TABLE activity_link ADD CONSTRAINT activity_link_shape CHECK (
  (entity_type='person'       AND person_id IS NOT NULL AND organization_id IS NULL AND deal_id IS NULL) OR
  (entity_type='organization' AND organization_id IS NOT NULL AND person_id IS NULL AND deal_id IS NULL) OR
  (entity_type='deal'         AND deal_id IS NOT NULL AND person_id IS NULL AND organization_id IS NULL)
);
ALTER TABLE activity_link DROP CONSTRAINT activity_link_entity_type_check;
ALTER TABLE activity_link ADD CONSTRAINT activity_link_entity_type_check
  CHECK (entity_type IN ('person','organization','deal'));
ALTER TABLE activity_link DROP COLUMN lead_id;
