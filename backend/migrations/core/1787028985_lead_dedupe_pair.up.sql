-- Leads dedupe against LEADS, in the same review queue (ADR-0118/A169 §2).
--
-- dedupe_candidate was built polymorphic over the deduped record types —
-- a discriminator plus per-type nullable FKs — precisely so a third type
-- costs a column pair and a CHECK widening, not a table. A pair is always
-- same-type: the shape CHECK is what keeps "a lead is never proposed as a
-- duplicate of a person" true in the schema rather than in a handler.

ALTER TABLE dedupe_candidate
  ADD COLUMN left_lead_id  uuid NULL,
  ADD COLUMN right_lead_id uuid NULL;

-- Single-column keys, as every FK has been since 0218/0225 collapsed the
-- composite ones.
ALTER TABLE dedupe_candidate
  ADD CONSTRAINT dedupe_candidate_left_lead_id_fkey FOREIGN KEY (left_lead_id)
    REFERENCES lead (id) ON DELETE CASCADE,
  ADD CONSTRAINT dedupe_candidate_right_lead_id_fkey FOREIGN KEY (right_lead_id)
    REFERENCES lead (id) ON DELETE CASCADE;

ALTER TABLE dedupe_candidate DROP CONSTRAINT dedupe_candidate_entity_type_check;
ALTER TABLE dedupe_candidate
  ADD CONSTRAINT dedupe_candidate_entity_type_check
    CHECK (entity_type IN ('person','organization','lead'));

ALTER TABLE dedupe_candidate DROP CONSTRAINT dedupe_candidate_shape;
ALTER TABLE dedupe_candidate
  ADD CONSTRAINT dedupe_candidate_shape CHECK (
    (entity_type = 'person'
       AND left_person_id IS NOT NULL AND right_person_id IS NOT NULL
       AND left_org_id IS NULL AND right_org_id IS NULL
       AND left_lead_id IS NULL AND right_lead_id IS NULL)
    OR (entity_type = 'organization'
       AND left_org_id IS NOT NULL AND right_org_id IS NOT NULL
       AND left_person_id IS NULL AND right_person_id IS NULL
       AND left_lead_id IS NULL AND right_lead_id IS NULL)
    OR (entity_type = 'lead'
       AND left_lead_id IS NOT NULL AND right_lead_id IS NOT NULL
       AND left_person_id IS NULL AND right_person_id IS NULL
       AND left_org_id IS NULL AND right_org_id IS NULL)
  );

ALTER TABLE dedupe_candidate DROP CONSTRAINT dedupe_candidate_ordered;
ALTER TABLE dedupe_candidate
  ADD CONSTRAINT dedupe_candidate_ordered CHECK (
    coalesce(left_person_id, left_org_id, left_lead_id) < coalesce(right_person_id, right_org_id, right_lead_id)
  );

-- One row per pair, forever — the suppression (AC-dedupe-7) — now over the
-- lead pair too.
DROP INDEX uq_dedupe_candidate_pair;
CREATE UNIQUE INDEX uq_dedupe_candidate_pair ON dedupe_candidate
  (entity_type,
   coalesce(left_person_id, left_org_id, left_lead_id),
   coalesce(right_person_id, right_org_id, right_lead_id));

-- The merge verb's provenance: the loser of a lead↔lead merge keeps the
-- pointer to the record it folded into, the way person.merged_into_id does.
ALTER TABLE lead ADD COLUMN merged_into_id uuid NULL;
ALTER TABLE lead
  ADD CONSTRAINT lead_merged_into_id_fkey FOREIGN KEY (merged_into_id)
    REFERENCES lead (id) ON DELETE SET NULL (merged_into_id);
CREATE INDEX idx_lead_merged_into ON lead (merged_into_id) WHERE merged_into_id IS NOT NULL;
