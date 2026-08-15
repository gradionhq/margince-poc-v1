-- 0242: a message a rep chose to send later.
--
-- ADR-0104/A155. This is NOT a due-date column on comms_outbound, and the
-- reason is worth stating where the schema is, because the column is the
-- obvious design and it breaks three things.
--
-- A comms_outbound row is a message the system is TRYING to send: the
-- dispatcher counts its attempts, ages it out after a day (a permanently
-- deferred delivery is indistinguishable from a lost one), and every gate
-- above it has already answered. A message due Friday is not being tried yet.
-- Modelling it as a delivery would age it out on Tuesday, would need an
-- activity written for a message nobody sent (ADR-0087 forbids exactly that),
-- and would carry a minutes-scale approval token across days (ADR-0036).
--
-- So a scheduled send holds its own frozen payload and writes NOTHING to the
-- timeline until it fires. At fire it replays through the one send path, and
-- the activity, the delivery row and the dispatch job are all created THEN —
-- which is why the dispatcher's max-age guard needs no change: it still
-- measures a delivery from the moment the delivery was made.
CREATE TABLE scheduled_send (
  id                  uuid PRIMARY KEY,
  workspace_id        uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  -- RESTRICT matches comms_outbound: a scheduled message pointing at a
  -- vanished workspace has nothing honest to say about who it belongs to.

  status              text NOT NULL DEFAULT 'scheduled',
  scheduled_at        timestamptz NOT NULL,
  -- The IANA zone the human PICKED the instant in. scheduled_at is absolute
  -- and settles when the message fires; this records the choice, so the UI
  -- re-renders "09:00 Monday" as the rep meant it and an auditor can see
  -- which wall clock was intended. AC-DS-TZ4 bans fixed numeric offsets, and
  -- a zone name is not one. ADR-0104 §7 states why an absolute instant is
  -- correct for a ONE-SHOT and must not be inherited by recurrence.
  scheduled_tz        text NOT NULL,

  -- The origin this send replays with (ADR-0087's two origins). A reply
  -- carries its anchor; an account-started message carries its links. Exactly
  -- one shape is populated, enforced below — a row holding both would be a
  -- send with two different ideas of what it is attached to.
  origin_kind         text NOT NULL,
  anchor_activity_id  uuid NULL REFERENCES activity(id) ON DELETE CASCADE,
  -- CASCADE: a scheduled reply to a deleted activity has no thread to join.
  -- The fire path re-resolves the anchor anyway and holds when it is gone;
  -- this is the DDL-level backstop, not the user-visible behaviour.
  origin_links        jsonb NULL,

  -- The message itself, frozen at schedule time. A VERSIONED payload rather
  -- than a serialized Go struct: these rows outlive the code that wrote them
  -- by up to the scheduling ceiling, and a field rename in a later refactor
  -- must not silently change what a pending message says.
  payload             jsonb NOT NULL,
  payload_version     int NOT NULL DEFAULT 1,

  -- Who authorized this send, and what KIND of principal will execute it.
  -- Two columns because they answer different questions and collapsing them
  -- is a real defect: the send path withholds a human's sign-off and display
  -- name when an agent is the actor (signature.go, sendername.go — "a
  -- tool-written message arriving under somebody's personal sign-off claims a
  -- hand that never touched it"). An agent-scheduled message fired under a
  -- reconstructed HUMAN principal would acquire the approver's signature that
  -- the identical immediate send would never carry. ADR-0104 §4.
  scheduled_by        uuid NOT NULL REFERENCES app_user(id) ON DELETE RESTRICT,
  principal_kind      text NOT NULL,

  -- What the fire produced, once it has fired. activity_id is the timeline
  -- row; delivery_id is the comms_outbound row that owns delivery truth from
  -- there on.
  activity_id         uuid NULL REFERENCES activity(id) ON DELETE SET NULL,
  delivery_id         uuid NULL,
  -- Why a human has to look at it. Set only with status 'held'.
  held_reason         text NULL,

  version             bigint NOT NULL DEFAULT 1,
  created_at          timestamptz NOT NULL DEFAULT now(),
  updated_at          timestamptz NOT NULL DEFAULT now(),

  -- 'released' is deliberately not 'sent'. At the end of the fire transaction
  -- the provider has NOT been called: a pending delivery row and a dispatch
  -- job exist, and the dispatcher can still park or fail it. Calling this
  -- 'sent' would put a claim in the schema that the schema cannot back, and
  -- delivery truth already has a home on comms_outbound.
  CONSTRAINT scheduled_send_status
    CHECK (status IN ('scheduled','released','cancelled','held')),
  CONSTRAINT scheduled_send_origin_kind
    CHECK (origin_kind IN ('reply','account')),
  CONSTRAINT scheduled_send_principal_kind
    CHECK (principal_kind IN ('human','agent')),

  -- Exactly one origin shape, matching origin_kind. Without this a row can
  -- claim to be a reply while carrying account links, and the fire path would
  -- have to choose which half to believe.
  CONSTRAINT scheduled_send_origin_shape CHECK (
    (origin_kind = 'reply'   AND anchor_activity_id IS NOT NULL AND origin_links IS NULL)
    OR
    (origin_kind = 'account' AND anchor_activity_id IS NULL     AND origin_links IS NOT NULL)
  ),
  -- origin_links is a LIST when present. Same reasoning as comms_outbound's
  -- jsonb type checks: a nil Go slice encodes as JSON null, which is legal
  -- jsonb, and a loader cannot tell that from an empty list.
  CONSTRAINT scheduled_send_origin_links_shape
    CHECK (origin_links IS NULL OR jsonb_typeof(origin_links) = 'array'),

  -- State shape. A row that names an activity it has not produced, or a hold
  -- reason while still pending, is a row whose status and columns disagree —
  -- and the surface that renders it would have to pick one.
  CONSTRAINT scheduled_send_released_shape CHECK (
    (status = 'released' AND activity_id IS NOT NULL AND delivery_id IS NOT NULL)
    OR
    (status <> 'released' AND activity_id IS NULL AND delivery_id IS NULL)
  ),
  CONSTRAINT scheduled_send_held_shape CHECK (
    (status = 'held' AND held_reason IS NOT NULL)
    OR
    (status <> 'held' AND held_reason IS NULL)
  )
);

-- The due-scan the timer worker makes when it wakes: one pending row by id.
-- Partial on 'scheduled' because the other three states are terminal for the
-- timer and are read by the rep's list surface, not by the sweep.
CREATE INDEX idx_scheduled_send_due
  ON scheduled_send (workspace_id, scheduled_at)
  WHERE status = 'scheduled';

-- The rep's own list, newest intention first.
CREATE INDEX idx_scheduled_send_owner
  ON scheduled_send (workspace_id, scheduled_by, status, scheduled_at DESC);

-- A scheduled reply's anchor, for the person/thread surface that shows what
-- is pending against a conversation.
CREATE INDEX idx_scheduled_send_anchor
  ON scheduled_send (anchor_activity_id)
  WHERE anchor_activity_id IS NOT NULL;
