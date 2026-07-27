-- ADR-0072/A118 (PO-F-2a, the §2.9 signature-enrich amendment): the
-- per-person record of which mail the signature pass has already read.
--
-- The pass used to select a person only while they had NO person_profile_field
-- row at all, which meant one accepted field retired them forever — so a person
-- whose signature stated a title but no company could never contribute the
-- org_name the name-promotion rule needs. Considering a missing org_name
-- specifically reopens that person, and without a record of what was already
-- read the pass would re-ask the model about the same mail every night for
-- every person whose signature simply does not state one.
--
-- This table is that record: the activity whose signature block was last shown
-- to the model. A person is a candidate again only when newer mail arrives, so
-- the cost of asking is bounded by mail volume rather than by time.
--
-- The comparison is on TIME, not on identity, and the cursor only ever moves
-- forward. Asking "is the newest mail a different row than the one I read" would
-- reopen a person the moment the mail that was read is archived — the next
-- candidate query then finds an OLDER message and pays for reading a signature
-- that has already been read. Two workers finishing out of order would rewind it
-- the same way.
--
-- Sidecar rather than a column on person, deliberately: person carries the
-- set_updated_at_bump_version trigger, so stamping the attempt on the row would
-- bump the record's version and updated_at on every pass — manufacturing
-- version skew for every client holding a person, over a fact no reader of a
-- person cares about.
CREATE TABLE person_signature_enrich_state (
  workspace_id uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  person_id    uuid PRIMARY KEY,

  -- The activity whose trailing lines were sent to the model. The composite
  -- tenant reference the rest of the capture tables use, cascading on purpose:
  -- losing the activity honestly reopens the person for the next mail that
  -- arrives rather than leaving a cursor pointing at nothing.
  activity_id uuid NOT NULL,
  CONSTRAINT person_signature_enrich_state_activity_id_fkey
    FOREIGN KEY (workspace_id, activity_id)
    REFERENCES activity (workspace_id, id) ON DELETE CASCADE,

  CONSTRAINT person_signature_enrich_state_person_id_fkey
    FOREIGN KEY (workspace_id, person_id)
    REFERENCES person (workspace_id, id) ON DELETE CASCADE,

  -- The read watermark: the occurred_at of the newest mail already shown to the
  -- model. This is what the candidate query compares against, and it is written
  -- as a GREATEST so it never moves backwards — unlike activity_id, which names
  -- WHICH mail was read and is provenance only.
  last_activity_at timestamptz NOT NULL,

  attempted_at timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE person_signature_enrich_state ENABLE ROW LEVEL SECURITY;
ALTER TABLE person_signature_enrich_state FORCE ROW LEVEL SECURITY;
CREATE POLICY person_signature_enrich_state_tenant_isolation ON person_signature_enrich_state
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);
