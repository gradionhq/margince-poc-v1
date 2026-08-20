-- Reversing this is only safe once no row holds the value the narrower
-- constraint would refuse. Re-labelling those rows as 'other' rather than
-- deleting them keeps the file and loses only the distinction this migration
-- introduced — a channel file stays downloadable, it just stops saying which
-- kind of transport carried it. That loss is the honest cost of the reversal and
-- is not recoverable from the row alone; the activity it hangs off still names
-- its transport.
SET LOCAL lock_timeout = '3s';

UPDATE attachment SET category = 'other' WHERE category = 'message_attachment';

ALTER TABLE attachment DROP CONSTRAINT attachment_category_check;
ALTER TABLE attachment
  ADD CONSTRAINT attachment_category_check
  CHECK (category IN ('contract','offer','legal','email_attachment','other'));
