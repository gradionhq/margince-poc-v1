-- 0038: the lead arm of the activity timeline (formulas-and-rules §3 /
-- §2.2). Behavioral lead scoring reads replies and meetings from
-- activity rows LINKED TO THE LEAD — a linkage the polymorphic
-- activity_link never carried even though the contract's relink enum
-- admits 'lead' (filed as feedback/17). Same shape as the other three
-- arms: composite tenant FK, exactly-one-target CHECK, dedupe index.
ALTER TABLE activity_link ADD COLUMN lead_id uuid NULL;
ALTER TABLE activity_link ADD CONSTRAINT activity_link_lead_id_fkey
  FOREIGN KEY (workspace_id, lead_id) REFERENCES lead (workspace_id, id) ON DELETE CASCADE;

ALTER TABLE activity_link DROP CONSTRAINT activity_link_entity_type_check;
ALTER TABLE activity_link ADD CONSTRAINT activity_link_entity_type_check
  CHECK (entity_type IN ('person','organization','deal','lead'));

ALTER TABLE activity_link DROP CONSTRAINT activity_link_shape;
ALTER TABLE activity_link ADD CONSTRAINT activity_link_shape CHECK (
  (entity_type='person'       AND person_id IS NOT NULL AND organization_id IS NULL AND deal_id IS NULL AND lead_id IS NULL) OR
  (entity_type='organization' AND organization_id IS NOT NULL AND person_id IS NULL AND deal_id IS NULL AND lead_id IS NULL) OR
  (entity_type='deal'         AND deal_id IS NOT NULL AND person_id IS NULL AND organization_id IS NULL AND lead_id IS NULL) OR
  (entity_type='lead'         AND lead_id IS NOT NULL AND person_id IS NULL AND organization_id IS NULL AND deal_id IS NULL)
);

DROP INDEX uq_activity_link;
CREATE UNIQUE INDEX uq_activity_link
  ON activity_link (activity_id, entity_type, coalesce(person_id, organization_id, deal_id, lead_id));
CREATE INDEX idx_alink_lead ON activity_link (lead_id) WHERE lead_id IS NOT NULL;
