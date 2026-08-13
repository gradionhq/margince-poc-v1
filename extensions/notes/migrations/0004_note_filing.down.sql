-- Reverses 0004. The column goes and the receipts with it; the notes
-- themselves are 0001's and stay.
ALTER TABLE ext.ext_notes_note
    DROP COLUMN IF EXISTS filed_activity_id;
