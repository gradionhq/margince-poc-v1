DROP INDEX IF EXISTS idx_lead_merged_into;
ALTER TABLE lead DROP CONSTRAINT IF EXISTS lead_merged_into_id_fkey;
ALTER TABLE lead DROP COLUMN IF EXISTS merged_into_id;

DROP INDEX uq_dedupe_candidate_pair;
DELETE FROM dedupe_candidate WHERE entity_type = 'lead';
ALTER TABLE dedupe_candidate DROP CONSTRAINT dedupe_candidate_ordered;
ALTER TABLE dedupe_candidate
  ADD CONSTRAINT dedupe_candidate_ordered CHECK (
    coalesce(left_person_id, left_org_id) < coalesce(right_person_id, right_org_id)
  );
ALTER TABLE dedupe_candidate DROP CONSTRAINT dedupe_candidate_shape;
ALTER TABLE dedupe_candidate
  ADD CONSTRAINT dedupe_candidate_shape CHECK (
    (entity_type = 'person'
       AND left_person_id IS NOT NULL AND right_person_id IS NOT NULL
       AND left_org_id IS NULL AND right_org_id IS NULL)
    OR (entity_type = 'organization'
       AND left_org_id IS NOT NULL AND right_org_id IS NOT NULL
       AND left_person_id IS NULL AND right_person_id IS NULL)
  );
ALTER TABLE dedupe_candidate DROP CONSTRAINT dedupe_candidate_entity_type_check;
ALTER TABLE dedupe_candidate
  ADD CONSTRAINT dedupe_candidate_entity_type_check
    CHECK (entity_type IN ('person','organization'));
ALTER TABLE dedupe_candidate
  DROP CONSTRAINT dedupe_candidate_left_lead_id_fkey,
  DROP CONSTRAINT dedupe_candidate_right_lead_id_fkey,
  DROP COLUMN left_lead_id,
  DROP COLUMN right_lead_id;
CREATE UNIQUE INDEX uq_dedupe_candidate_pair ON dedupe_candidate
  (entity_type,
   coalesce(left_person_id, left_org_id), coalesce(right_person_id, right_org_id));
