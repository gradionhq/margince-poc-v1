-- Drops the run record for reading an attached document. The indexes go with
-- the table; the attachments themselves are untouched, since a reading was
-- only ever a projection of one.
DROP TABLE IF EXISTS attachment_extraction;
