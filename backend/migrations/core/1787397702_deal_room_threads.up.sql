-- The conversation inside a Deal Room: threads of comments, on a document or
-- on the room as a whole, and a buyer's decisions about a document version.
--
-- Unlike tasks and documents, none of this is editorial. A comment is live
-- collaboration: it reaches the other side the moment it commits, with no
-- publish in between, because a question nobody sees is not a question.
--
-- EXACTLY ONE AUTHOR. A row names either a participant (the buyer's side) or
-- an app_user (the seller's), never both and never neither — the CHECK refuses
-- a comment from nobody, which an audit could not attribute either.
CREATE TABLE deal_room_thread (
    id uuid DEFAULT uuidv7() NOT NULL,
    room_id uuid NOT NULL,
    -- NULL is a room-level exchange ("general update"); set, the thread is
    -- about one document, and attachment_id pins which VERSION it was opened on.
    document_id uuid,
    attachment_id uuid,
    -- A thread the buyer marks as requiring a change BLOCKS confirming the
    -- document's version until it is resolved.
    required_change boolean DEFAULT false NOT NULL,
    state text DEFAULT 'open'::text NOT NULL,
    author_participant_id uuid,
    author_user_id uuid,
    resolved_at timestamptz,
    resolved_by_user_id uuid,
    source text NOT NULL,
    captured_by text NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT deal_room_thread_pkey PRIMARY KEY (id),
    CONSTRAINT deal_room_thread_room_fkey FOREIGN KEY (room_id)
        REFERENCES deal_room(id) ON DELETE CASCADE,
    CONSTRAINT deal_room_thread_document_fkey FOREIGN KEY (document_id)
        REFERENCES deal_room_document(id) ON DELETE CASCADE,
    CONSTRAINT deal_room_thread_attachment_fkey FOREIGN KEY (attachment_id)
        REFERENCES attachment(id) ON DELETE RESTRICT,
    -- Composite: a thread's buyer author must belong to the thread's room.
    CONSTRAINT deal_room_thread_author_in_room
        FOREIGN KEY (author_participant_id, room_id)
        REFERENCES deal_room_participant(id, room_id) ON DELETE RESTRICT,
    CONSTRAINT deal_room_thread_author_user_fkey FOREIGN KEY (author_user_id)
        REFERENCES app_user(id) ON DELETE RESTRICT,
    CONSTRAINT deal_room_thread_resolver_fkey FOREIGN KEY (resolved_by_user_id)
        REFERENCES app_user(id) ON DELETE RESTRICT,
    CONSTRAINT deal_room_thread_state_check CHECK (state IN ('open', 'resolved')),
    CONSTRAINT deal_room_thread_one_author CHECK (
        (author_participant_id IS NOT NULL) <> (author_user_id IS NOT NULL)),
    CONSTRAINT deal_room_thread_document_has_version CHECK (
        (document_id IS NULL) = (attachment_id IS NULL)),
    CONSTRAINT deal_room_thread_resolution_complete CHECK (
        (state = 'open' AND resolved_at IS NULL AND resolved_by_user_id IS NULL)
        OR (state = 'resolved' AND resolved_at IS NOT NULL))
);

CREATE INDEX idx_deal_room_thread_room ON deal_room_thread (room_id, created_at);
CREATE INDEX idx_deal_room_thread_document_open ON deal_room_thread (document_id)
    WHERE state = 'open' AND required_change;

CREATE TRIGGER deal_room_thread_touch BEFORE UPDATE ON deal_room_thread
    FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();

-- Insert-only. A comment is what somebody said; it is never edited or deleted,
-- because a negotiation's record has to be able to say who said what when.
CREATE TABLE deal_room_comment (
    id uuid DEFAULT uuidv7() NOT NULL,
    room_id uuid NOT NULL,
    thread_id uuid NOT NULL,
    body text NOT NULL,
    author_participant_id uuid,
    author_user_id uuid,
    source text NOT NULL,
    captured_by text NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT deal_room_comment_pkey PRIMARY KEY (id),
    CONSTRAINT deal_room_comment_room_fkey FOREIGN KEY (room_id)
        REFERENCES deal_room(id) ON DELETE CASCADE,
    CONSTRAINT deal_room_comment_thread_fkey FOREIGN KEY (thread_id)
        REFERENCES deal_room_thread(id) ON DELETE CASCADE,
    CONSTRAINT deal_room_comment_author_in_room
        FOREIGN KEY (author_participant_id, room_id)
        REFERENCES deal_room_participant(id, room_id) ON DELETE RESTRICT,
    CONSTRAINT deal_room_comment_author_user_fkey FOREIGN KEY (author_user_id)
        REFERENCES app_user(id) ON DELETE RESTRICT,
    CONSTRAINT deal_room_comment_one_author CHECK (
        (author_participant_id IS NOT NULL) <> (author_user_id IS NOT NULL)),
    CONSTRAINT deal_room_comment_body_check CHECK (length(btrim(body)) > 0)
);

CREATE INDEX idx_deal_room_comment_thread ON deal_room_comment (thread_id, created_at);

-- A buyer's decision about one document VERSION. Insert-only: a later decision
-- is a new row, and the history of "asked for changes, then confirmed v3" is
-- exactly what a dispute reads. Explicitly NOT a legal signature.
CREATE TABLE deal_room_decision (
    id uuid DEFAULT uuidv7() NOT NULL,
    room_id uuid NOT NULL,
    document_id uuid NOT NULL,
    attachment_id uuid NOT NULL,
    participant_id uuid NOT NULL,
    kind text NOT NULL,
    note text,
    source text NOT NULL,
    captured_by text NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT deal_room_decision_pkey PRIMARY KEY (id),
    CONSTRAINT deal_room_decision_room_fkey FOREIGN KEY (room_id)
        REFERENCES deal_room(id) ON DELETE CASCADE,
    CONSTRAINT deal_room_decision_document_fkey FOREIGN KEY (document_id)
        REFERENCES deal_room_document(id) ON DELETE CASCADE,
    CONSTRAINT deal_room_decision_attachment_fkey FOREIGN KEY (attachment_id)
        REFERENCES attachment(id) ON DELETE RESTRICT,
    CONSTRAINT deal_room_decision_participant_in_room
        FOREIGN KEY (participant_id, room_id)
        REFERENCES deal_room_participant(id, room_id) ON DELETE RESTRICT,
    CONSTRAINT deal_room_decision_kind_check CHECK (kind IN ('request_changes', 'confirm_version'))
);

CREATE INDEX idx_deal_room_decision_document ON deal_room_decision (document_id, created_at DESC);
