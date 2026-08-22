SET LOCAL lock_timeout = '5s';
CREATE TABLE deal_room_task (
    id uuid DEFAULT uuidv7() NOT NULL,
    room_id uuid NOT NULL,
    side text NOT NULL,
    title text NOT NULL,
    position integer DEFAULT 0 NOT NULL,
    done_at timestamptz,
    -- Who ticked it. A buyer completion names the participant; a seller
    -- completion names the user; neither is set while the task is open.
    done_by_participant_id uuid,
    done_by_user_id uuid,
    source text NOT NULL,
    captured_by text NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    archived_at timestamptz,
    CONSTRAINT deal_room_task_pkey PRIMARY KEY (id),
    CONSTRAINT deal_room_task_room_fkey FOREIGN KEY (room_id)
        REFERENCES deal_room(id) ON DELETE CASCADE,
    -- Composite for the same reason the session is: a buyer from another room
    -- must not be able to appear as the person who completed this one's work.
    --
    -- RESTRICT, not SET NULL: the completion CHECK below forbids a done row with
    -- no actor, so SET NULL would fire on delete and then fail that CHECK,
    -- making the parent undeletable in a way nobody chose. Attribution outlives
    -- the actor here; clearing the completion is the writer's decision to make
    -- deliberately, not a cascade's.
    CONSTRAINT deal_room_task_participant_in_room
        FOREIGN KEY (done_by_participant_id, room_id)
        REFERENCES deal_room_participant(id, room_id) ON DELETE RESTRICT,
    CONSTRAINT deal_room_task_user_fkey FOREIGN KEY (done_by_user_id)
        REFERENCES app_user(id) ON DELETE RESTRICT,
    CONSTRAINT deal_room_task_side_check CHECK (side IN ('seller', 'buyer')),
    -- An open task names nobody; a done task names exactly one side's actor.
    -- Written as a constraint rather than trusted to the writer, because a
    -- half-set completion is unreadable: "done, by nobody" tells a reader less
    -- than either fact alone.
    CONSTRAINT deal_room_task_completion_check CHECK (
        (done_at IS NULL AND done_by_participant_id IS NULL AND done_by_user_id IS NULL)
        OR (done_at IS NOT NULL AND done_by_participant_id IS NOT NULL AND done_by_user_id IS NULL)
        OR (done_at IS NOT NULL AND done_by_user_id IS NOT NULL AND done_by_participant_id IS NULL))
);

CREATE INDEX idx_deal_room_task_room ON deal_room_task (room_id, position)
    WHERE archived_at IS NULL;

CREATE TRIGGER deal_room_task_touch BEFORE UPDATE ON deal_room_task
    FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();
