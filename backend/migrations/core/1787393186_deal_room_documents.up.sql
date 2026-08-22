-- The documents a seller puts in front of a buyer.
--
-- A room document POINTS AT an attachment already filed on the room's deal;
-- it never holds bytes of its own, and a buyer never uploads one. The
-- attachment row IS the version (attachment.supersedes_id relates versions),
-- so attachment_id anchors the exact file a buyer was shown. Replacing a
-- document with a newer version is a new row; the old one is removed.
--
-- group_key is one of four fixed machine keys, never a display label: the
-- labels are i18n, and a key coupled to English copy breaks the moment the
-- copy changes. There is no configurable grouping and no AI filing.
--
-- Like a task DEFINITION, a document is editorial: it reaches the buyer through
-- the release that publishes it, and the manifest a release freezes is what the
-- buyer edge serves — never this table directly.
CREATE TABLE deal_room_document (
    id uuid DEFAULT uuidv7() NOT NULL,
    room_id uuid NOT NULL,
    attachment_id uuid NOT NULL,
    group_key text NOT NULL,
    -- The buyer-facing name. Defaults to the attachment's filename at add time
    -- and is editable, because "DPA_v7_final_FINAL.pdf" is not what a buyer
    -- should read.
    title text NOT NULL,
    position integer DEFAULT 0 NOT NULL,
    archived_at timestamptz,
    source text NOT NULL,
    captured_by text NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT deal_room_document_pkey PRIMARY KEY (id),
    CONSTRAINT deal_room_document_room_fkey FOREIGN KEY (room_id)
        REFERENCES deal_room(id) ON DELETE CASCADE,
    -- RESTRICT: an attachment a buyer was shown cannot vanish from under the
    -- manifest that names it. Removing the document is the seller's act.
    CONSTRAINT deal_room_document_attachment_fkey FOREIGN KEY (attachment_id)
        REFERENCES attachment(id) ON DELETE RESTRICT,
    CONSTRAINT deal_room_document_group_check
        CHECK (group_key IN ('commercial', 'legal', 'security_privacy', 'delivery_operations')),
    CONSTRAINT deal_room_document_title_check CHECK (length(btrim(title)) > 0)
);

-- One live row per file per room: the same version shown twice is one entry.
CREATE UNIQUE INDEX uq_deal_room_document_live ON deal_room_document (room_id, attachment_id)
    WHERE archived_at IS NULL;

CREATE INDEX idx_deal_room_document_room ON deal_room_document (room_id, group_key, position)
    WHERE archived_at IS NULL;

CREATE TRIGGER deal_room_document_touch BEFORE UPDATE ON deal_room_document
    FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();

