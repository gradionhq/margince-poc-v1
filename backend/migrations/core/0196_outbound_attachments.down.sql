-- Reverses 0196. The snapshot is dropped with its shape constraint; the
-- documents themselves are untouched, because this column only ever described
-- what one message carried.
ALTER TABLE comms_outbound DROP CONSTRAINT IF EXISTS comms_outbound_attachments_shape;
ALTER TABLE comms_outbound DROP COLUMN IF EXISTS attachments;
DROP FUNCTION IF EXISTS comms_outbound_attachments_well_formed(jsonb);
