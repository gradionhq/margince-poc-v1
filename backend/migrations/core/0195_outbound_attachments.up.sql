-- 0195: an outbound message records the files it carried (ADR-0086/A131 §4).
--
-- The set is SNAPSHOTTED here rather than being a list of attachment ids the
-- reader dereferences. Archiving or superseding a document later changes what
-- the library shows and must change nothing about what the timeline says was
-- attached to a message that already went out. A pointer would let a later edit
-- rewrite history; a snapshot cannot.
--
-- The ids are kept alongside the snapshot so the transmit-time recheck can ask
-- about each file's current availability, scan state and visibility — the check
-- that runs because a file clean at staging can be blocked by the time the job
-- runs. What is RECORDED is the snapshot; what is RECHECKED is the live row.
ALTER TABLE comms_outbound
  ADD COLUMN attachments jsonb NOT NULL DEFAULT '[]'::jsonb;

-- One shape, so a reader never has to guess whether a key is missing or the
-- whole column is malformed. Each entry names the file it WAS, not the file it
-- is now.
--
-- Through an IMMUTABLE function rather than inline: a CHECK cannot carry a
-- subquery, and jsonb_array_elements is a set-returning function that only
-- reads as one inside a query. The function is the same predicate written where
-- Postgres will accept it.
CREATE OR REPLACE FUNCTION comms_outbound_attachments_well_formed(files jsonb)
RETURNS boolean LANGUAGE sql IMMUTABLE AS $$
  SELECT jsonb_typeof(files) = 'array'
     AND NOT EXISTS (
       SELECT 1 FROM jsonb_array_elements(files) AS f
        WHERE jsonb_typeof(f) <> 'object'
           OR f->>'attachment_id' IS NULL
           OR f->>'filename' IS NULL
     );
$$;

ALTER TABLE comms_outbound
  ADD CONSTRAINT comms_outbound_attachments_shape
  CHECK (comms_outbound_attachments_well_formed(attachments));

COMMENT ON COLUMN comms_outbound.attachments IS
  'Immutable snapshot of what was attached (ADR-0086 §4): filename, type, size, checksum. Never rewritten by a later change to the document.';
