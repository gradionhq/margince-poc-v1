-- The Deal Room spine: one buyer-facing room per deal, its immutable published
-- releases, the named people invited into it, the credentials that admit them,
-- and the shared to-do list both sides work from.
--
-- The room is a PROJECTION, not a second copy of the deal. Nothing here
-- duplicates a CRM record: the room points at a deal, documents point at
-- attachment rows the seller already curated, and the release freezes the exact
-- editorial text a buyer was shown. That is what makes the public edge safe to
-- serve — it reads a release, never the live deal.
--
-- NO workspace_id, on purpose: ADR-0091 phase D removed it from every domain
-- table, so a business key is unique on its own here.

-- The room itself. One logical room per deal, many releases over its life.
CREATE TABLE deal_room (
    id uuid DEFAULT uuidv7() NOT NULL,
    deal_id uuid NOT NULL,
    title text NOT NULL,
    welcome_message text,
    state text DEFAULT 'draft'::text NOT NULL,
    -- The named human a buyer reaches for help. Defaults to the deal owner and
    -- transfers deliberately, because a room whose steward left the company
    -- points its buyers at nobody.
    steward_user_id uuid,
    -- Access ends on its own. Extending is an explicit human act, so this is a
    -- fact about the room rather than a policy read at request time.
    expires_at timestamptz,
    published_at timestamptz,
    closed_at timestamptz,
    source text NOT NULL,
    captured_by text NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    archived_at timestamptz,
    CONSTRAINT deal_room_pkey PRIMARY KEY (id),
    CONSTRAINT deal_room_deal_id_fkey FOREIGN KEY (deal_id)
        REFERENCES deal(id) ON DELETE CASCADE,
    CONSTRAINT deal_room_steward_fkey FOREIGN KEY (steward_user_id)
        REFERENCES app_user(id) ON DELETE SET NULL,
    CONSTRAINT deal_room_state_check CHECK (state IN (
        'draft', 'building', 'ready', 'publishing',
        'live', 'paused', 'closed', 'expired', 'archived')),
    -- The two ways this row can say "archived" must agree. Left apart, a row
    -- could read live to the state machine and archived to every list query
    -- that filters on archived_at, which is the repo's normal convention.
    CONSTRAINT deal_room_archived_agrees
        CHECK ((state = 'archived') = (archived_at IS NOT NULL))
);

-- ONE active room per deal. The predicate names every non-terminal state, not
-- just the obvious three: a second room created while the first is building,
-- ready or publishing is the same "which link is current?" bug arriving a few
-- seconds earlier.
CREATE UNIQUE INDEX uq_deal_room_active ON deal_room (deal_id)
    WHERE state IN ('draft', 'building', 'ready', 'publishing', 'live', 'paused')
      AND archived_at IS NULL;

-- An immutable published snapshot. Every buyer-visible editorial value is
-- COPIED in, so a later edit to the deal cannot change what a buyer was shown,
-- and "what exactly did they see?" has an answer that does not depend on the
-- current state of the CRM.
CREATE TABLE deal_room_release (
    id uuid DEFAULT uuidv7() NOT NULL,
    room_id uuid NOT NULL,
    release_no integer NOT NULL,
    -- The frozen buyer projection: status sentence, next step, welcome text,
    -- seller display fields, the document manifest, and the task definitions.
    -- Completion state is deliberately NOT in here (see deal_room_task).
    snapshot jsonb NOT NULL,
    release_note text,
    published_by uuid,
    published_at timestamptz DEFAULT now() NOT NULL,
    source text NOT NULL,
    captured_by text NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT deal_room_release_pkey PRIMARY KEY (id),
    CONSTRAINT deal_room_release_room_fkey FOREIGN KEY (room_id)
        REFERENCES deal_room(id) ON DELETE CASCADE,
    CONSTRAINT deal_room_release_published_by_fkey FOREIGN KEY (published_by)
        REFERENCES app_user(id) ON DELETE SET NULL,
    CONSTRAINT uq_deal_room_release_no UNIQUE (room_id, release_no),
    -- Every consumer reads this as a projection object; jsonb would otherwise
    -- accept a bare string or array and fail far from here.
    CONSTRAINT deal_room_release_snapshot_is_object
        CHECK (jsonb_typeof(snapshot) = 'object')
);

-- One named person admitted to one room. Not an app_user, not a seat: a buyer
-- consumes no licence and has no CRM authority whatsoever.
CREATE TABLE deal_room_participant (
    id uuid DEFAULT uuidv7() NOT NULL,
    room_id uuid NOT NULL,
    full_name text NOT NULL,
    -- Stored lowercase and checked, the way person.email and lead.email are.
    -- The uniqueness index below then compares raw values without needing a
    -- functional index or citext, neither of which this schema uses.
    email text NOT NULL,
    -- Coarse and room-wide on purpose. A per-document permission matrix is a
    -- product nobody asked for; 'reviewer' is the only one that can confirm a
    -- document version and it is granted by a human, never by default.
    capability text DEFAULT 'view'::text NOT NULL,
    invited_by uuid,
    revoked_at timestamptz,
    source text NOT NULL,
    captured_by text NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT deal_room_participant_pkey PRIMARY KEY (id),
    CONSTRAINT deal_room_participant_room_fkey FOREIGN KEY (room_id)
        REFERENCES deal_room(id) ON DELETE CASCADE,
    CONSTRAINT deal_room_participant_invited_by_fkey FOREIGN KEY (invited_by)
        REFERENCES app_user(id) ON DELETE SET NULL,
    CONSTRAINT deal_room_participant_capability_check
        CHECK (capability IN ('view', 'comment', 'reviewer')),
    CONSTRAINT deal_room_participant_email_norm CHECK (email = lower(email)),
    -- Not redundant with the primary key: it is the target a child row binds to
    -- when it must prove its participant belongs to the SAME room it names.
    CONSTRAINT uq_deal_room_participant_in_room UNIQUE (id, room_id)
);

-- One live seat per address. A revoked participant keeps their row so their
-- comments stay attributed, and the same address can be invited again.
CREATE UNIQUE INDEX uq_deal_room_participant_email ON deal_room_participant (room_id, email)
    WHERE revoked_at IS NULL;

-- An invitation ATTEMPT. Delivery state is modelled apart from access state
-- because a seller needs to answer "did it arrive?" separately from "may they
-- in?" — a bounced invitation and a revoked one look identical otherwise.
CREATE TABLE deal_room_invitation (
    id uuid DEFAULT uuidv7() NOT NULL,
    participant_id uuid NOT NULL,
    attempt_no integer DEFAULT 1 NOT NULL,
    -- Only the hash is stored. The raw credential exists in the delivered mail
    -- and nowhere else, so this table cannot re-admit anyone if it leaks.
    token_hash bytea NOT NULL,
    expires_at timestamptz NOT NULL,
    sent_at timestamptz,
    delivered_at timestamptz,
    failed_at timestamptz,
    failure_reason text,
    consumed_at timestamptz,
    superseded_at timestamptz,
    source text NOT NULL,
    captured_by text NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT deal_room_invitation_pkey PRIMARY KEY (id),
    CONSTRAINT deal_room_invitation_participant_fkey FOREIGN KEY (participant_id)
        REFERENCES deal_room_participant(id) ON DELETE CASCADE,
    CONSTRAINT uq_deal_room_invitation_token UNIQUE (token_hash),
    CONSTRAINT uq_deal_room_invitation_attempt UNIQUE (participant_id, attempt_no)
);

-- At most ONE live credential per participant. Resending supersedes rather than
-- accumulates, so an old link in an old mailbox stops working the moment a new
-- one is sent.
CREATE UNIQUE INDEX uq_deal_room_invitation_live ON deal_room_invitation (participant_id)
    WHERE consumed_at IS NULL AND superseded_at IS NULL;

-- A room-scoped browser session, exchanged once from an invitation. Resolved
-- fresh on every request: that read IS the revocation guarantee, because a
-- cached session would keep answering after the seller withdrew access.
CREATE TABLE deal_room_session (
    id uuid DEFAULT uuidv7() NOT NULL,
    participant_id uuid NOT NULL,
    room_id uuid NOT NULL,
    token_hash bytea NOT NULL,
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    last_seen_at timestamptz,
    source text NOT NULL,
    captured_by text NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT deal_room_session_pkey PRIMARY KEY (id),
    CONSTRAINT deal_room_session_room_fkey FOREIGN KEY (room_id)
        REFERENCES deal_room(id) ON DELETE CASCADE,
    -- COMPOSITE, and this is the load-bearing one: two independent foreign keys
    -- would let a session name room A while its participant belongs to room B,
    -- and a resolver that trusted room_id would then serve one room's content to
    -- another room's buyer. The database refuses the pairing outright rather
    -- than leaving it to every future query to re-check.
    CONSTRAINT deal_room_session_participant_in_room
        FOREIGN KEY (participant_id, room_id)
        REFERENCES deal_room_participant(id, room_id) ON DELETE CASCADE,
    CONSTRAINT uq_deal_room_session_token UNIQUE (token_hash)
);

CREATE INDEX idx_deal_room_session_participant ON deal_room_session (participant_id)
    WHERE revoked_at IS NULL;

-- The shared to-do list. Both sides see one list, split by who owes the work.
--
-- THE SPLIT THAT MATTERS: a task's DEFINITION (title, side, order) is editorial
-- content and rides a release like everything else a buyer reads, so a rep
-- cannot silently change what the buyer is being asked to do. Its COMPLETION
-- (done_at) is live collaboration state both sides toggle without republishing.
-- Freezing completion would mean either a buyer's tick never appearing or the
-- room republishing on every checkbox; publishing nothing would let the asks
-- themselves change unseen. Neither is acceptable, so they are separated here.
--
-- No due date column, deliberately. Dates were ruled out of the buyer
-- experience, and an unused column is an invitation to start using one.
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

-- The version/updated_at contract. A store that patches WHERE version = $n
-- needs the row to bump its own version, or a stale write passes the guard and
-- silently overwrites a change the caller never saw.
CREATE TRIGGER deal_room_touch BEFORE UPDATE ON deal_room
    FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();
CREATE TRIGGER deal_room_task_touch BEFORE UPDATE ON deal_room_task
    FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();

-- A participant carries no version column (nothing patches it optimistically),
-- so it takes the plain touch.
CREATE TRIGGER deal_room_participant_touch BEFORE UPDATE ON deal_room_participant
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- A release is immutable, and saying so in prose is not enough: the app role
-- holds ordinary CRUD on every table, so one buggy writer could rewrite what a
-- buyer was actually shown and leave no trace that it changed. The whole value
-- of a release is that it answers "what did they see in August?" without
-- trusting anything written since, so the database refuses the edit.
--
-- DELETE is allowed only as the room's CASCADE: by the time this fires for a
-- cascade the parent room is already gone, which is exactly when removing the
-- release is legitimate. A direct delete finds the room still there and fails.
CREATE FUNCTION deal_room_release_is_frozen() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
  IF TG_OP = 'UPDATE' THEN
    RAISE EXCEPTION 'deal_room_release is immutable: publish a new release instead of editing release % of room %', OLD.release_no, OLD.room_id
      USING ERRCODE = 'restrict_violation';
  END IF;

  IF EXISTS (SELECT 1 FROM deal_room r WHERE r.id = OLD.room_id) THEN
    RAISE EXCEPTION 'deal_room_release is immutable: release % of room % goes only with its room', OLD.release_no, OLD.room_id
      USING ERRCODE = 'restrict_violation';
  END IF;

  RETURN OLD;
END;
$$;

CREATE TRIGGER deal_room_release_frozen BEFORE UPDATE OR DELETE ON deal_room_release
    FOR EACH ROW EXECUTE FUNCTION deal_room_release_is_frozen();

-- Reach the installations that already exist. seedSystemRoles writes each role
-- document once at workspace creation and never re-syncs, so an object added to
-- the compiled defaults alone is granted to nobody who bootstrapped earlier — it
-- works on a fresh database and 403s everywhere else, permanently. The grants
-- below are the same ones the compiled defaults carry.
UPDATE role SET permissions = jsonb_set(
        permissions, '{objects,deal_room}',
        '{"create": true, "read": true, "update": true, "delete": true}'::jsonb, true)
    WHERE key IN ('admin', 'ops', 'manager', 'management')
      AND permissions ? 'objects'
      AND NOT (permissions -> 'objects') ? 'deal_room';

UPDATE role SET permissions = jsonb_set(
        permissions, '{objects,deal_room}',
        '{"create": true, "read": true, "update": true, "delete": false}'::jsonb, true)
    WHERE key = 'rep'
      AND permissions ? 'objects'
      AND NOT (permissions -> 'objects') ? 'deal_room';

UPDATE role SET permissions = jsonb_set(
        permissions, '{objects,deal_room}',
        '{"create": false, "read": true, "update": false, "delete": false}'::jsonb, true)
    WHERE key = 'read_only'
      AND permissions ? 'objects'
      AND NOT (permissions -> 'objects') ? 'deal_room';
