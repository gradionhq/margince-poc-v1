-- A deal's Files area lists its own uploads and the files of every message
-- linked to it. A captured file belongs to its message, so taking it off the
-- deal is a hide, never an archive: the row here says "not on this deal", and
-- the attachment, the activity and the company library are untouched.
SET LOCAL lock_timeout = '5s';
CREATE TABLE deal_document_hide (
    deal_id uuid NOT NULL,
    attachment_id uuid NOT NULL,
    hidden_by text NOT NULL,
    hidden_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT deal_document_hide_pkey PRIMARY KEY (deal_id, attachment_id),
    CONSTRAINT deal_document_hide_deal_fkey FOREIGN KEY (deal_id)
        REFERENCES deal(id) ON DELETE CASCADE,
    CONSTRAINT deal_document_hide_attachment_fkey FOREIGN KEY (attachment_id)
        REFERENCES attachment(id) ON DELETE CASCADE
);
