-- DH-DDL-1 (ADR-0062/A108): the dedupe review queue's own table — a
-- dedicated table, not an approval-inbox specialization: a not_a_duplicate
-- verdict is a durable fact about a PAIR that suppresses every future sweep,
-- whereas the inbox models one transient pending action.

CREATE TABLE dedupe_candidate (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id  uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,

  -- The pair. Polymorphic over the two deduped record types, shaped like
  -- activity_link (ACT-DDL-2): a discriminator plus per-type nullable FKs, so
  -- every id keeps a real foreign key instead of an untyped uuid.
  entity_type       text NOT NULL CHECK (entity_type IN ('person','organization')),
  left_person_id    uuid NULL REFERENCES person(id) ON DELETE CASCADE,
  right_person_id   uuid NULL REFERENCES person(id) ON DELETE CASCADE,
  left_org_id       uuid NULL REFERENCES organization(id) ON DELETE CASCADE,
  right_org_id      uuid NULL REFERENCES organization(id) ON DELETE CASCADE,

  confidence    numeric(4,3) NOT NULL CHECK (confidence >= 0 AND confidence <= 1),
  -- What the queue renders (AC-dedupe-2/3): per-field agree/collide evidence and
  -- the score's arithmetic, captured at detection so the queue shows what the
  -- detector actually saw, not a re-derivation against since-edited rows.
  evidence      jsonb NOT NULL,

  -- open → the human has not decided. merged → PO's merge path ran (the survivor
  -- is the live record; the loser carries merged_into_id). not_a_duplicate → the
  -- pair is suppressed from every future sweep (AC-dedupe-7).
  disposition   text NOT NULL DEFAULT 'open'
                CHECK (disposition IN ('open','merged','not_a_duplicate')),
  disposed_by   uuid NULL REFERENCES app_user(id) ON DELETE SET NULL,
  disposed_at   timestamptz NULL,

  source        text NOT NULL,   -- DM-CONV-11: which detector proposed the pair
  captured_by   text NOT NULL,
  raw           jsonb NULL,

  version       bigint NOT NULL DEFAULT 1,
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now(),
  archived_at   timestamptz NULL,

  -- Exactly the id pair the discriminator names, and no other.
  CONSTRAINT dedupe_candidate_shape CHECK (
    (entity_type = 'person'
       AND left_person_id IS NOT NULL AND right_person_id IS NOT NULL
       AND left_org_id IS NULL AND right_org_id IS NULL)
    OR (entity_type = 'organization'
       AND left_org_id IS NOT NULL AND right_org_id IS NOT NULL
       AND left_person_id IS NULL AND right_person_id IS NULL)
  ),
  -- A pair is unordered: store it canonically (lower id left) so {A,B} and {B,A}
  -- cannot both exist. Without this the unique index below is bypassable by
  -- re-detecting the pair from the other side, and suppression leaks.
  CONSTRAINT dedupe_candidate_ordered CHECK (
    coalesce(left_person_id, left_org_id) < coalesce(right_person_id, right_org_id)
  ),
  CONSTRAINT dedupe_candidate_disposed_shape CHECK (
    (disposition = 'open') = (disposed_at IS NULL)
  )
);

-- One row per pair, forever — this IS the suppression (AC-dedupe-7): a sweep
-- re-detecting a dismissed pair meets the existing not_a_duplicate row and
-- re-proposes nothing. Deliberately NOT partial on disposition.
CREATE UNIQUE INDEX uq_dedupe_candidate_pair ON dedupe_candidate
  (workspace_id, entity_type,
   coalesce(left_person_id, left_org_id), coalesce(right_person_id, right_org_id));

CREATE INDEX idx_dedupe_candidate_open ON dedupe_candidate
  (workspace_id, confidence DESC) WHERE disposition = 'open' AND archived_at IS NULL;

CREATE TRIGGER trg_dedupe_candidate_updated BEFORE UPDATE ON dedupe_candidate
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

ALTER TABLE dedupe_candidate ENABLE ROW LEVEL SECURITY;
ALTER TABLE dedupe_candidate FORCE ROW LEVEL SECURITY;
CREATE POLICY dedupe_candidate_tenant_isolation ON dedupe_candidate
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);
