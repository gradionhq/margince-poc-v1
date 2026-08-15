-- 0257: what the capture pipeline DID with one message, for 24 hours.
--
-- The pipeline is fully instrumented for writes and invisible to the people it
-- acts for: a member whose message never reached the timeline has no way to ask
-- why. audit_log records the activity that WAS created; the decisions that
-- created nothing -- the internal-only drop, the transactional suppression, the
-- deferral, the verdict -- are system_log breadcrumbs with no user attribution,
-- no read surface and, until now, no retention at all.
--
-- This table is the answer, and it is a DIAGNOSTIC TRACE rather than a record:
-- nothing links to it, nothing derives from it, and the sweep deletes it after
-- 24 hours. That is also why it writes no audit row of its own -- one per
-- captured message would double the ledger to say a message was captured, which
-- audit_log already says.
--
-- NO ROW LEVEL SECURITY, and that is not an omission: 0217 (ADR-0091 §8)
-- retired RLS for core tables, and §4 of it is explicit that a query which
-- forgets its scope predicate now returns other users' rows rather than zero.
-- Every read of this table spells its own workspace predicate, and the two that
-- matter more spell a user predicate too (see below).

CREATE TABLE capture_trace (
  id             uuid PRIMARY KEY DEFAULT uuidv7(),

  -- CASCADE rather than RESTRICT, unlike scheduled_send and transcript_read:
  -- those hold work somebody scheduled, and losing them silently with a
  -- workspace would lose a promise. A trace is an explanation of something that
  -- already happened, and a workspace being deleted must not fail on rows whose
  -- whole lifetime is 24 hours.
  workspace_id   uuid NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,

  -- WHOSE traffic this was, and the access-control axis of the whole feature.
  --
  --   NOT NULL -> a personal connection (connector_connection.granted_by, or an
  --               extension unit's own per-member row). The row is that
  --               member's alone; no grant widens it, because a colleague's
  --               mail arriving is not workspace configuration.
  --   NULL     -> a workspace-owned connection (channel_connection: a Telegram
  --               bot, a Zalo OA). One binding carries every seat's traffic, so
  --               it is a manager's to read.
  --
  -- Derived from the acting principal rather than declared: a personal ingest
  -- binds OnBehalfOf, a channel poll binds none. No FK to app_user -- an
  -- extension unit's ingress supplies a member id from a schema whose role
  -- holds no REFERENCES grant on public, and a core-only FK would make one
  -- column mean two different things. Every read joins nothing on it.
  user_id        uuid NULL,

  -- The PROVIDER ID, never a display label. A label is title-cased or compiled
  -- into the composition root, so it is a property of the running binary: two
  -- deploys' traces would disagree about the same transport with no row having
  -- changed. The id is the stable fact and the only spelling that can join to
  -- channel_provider and activity.channel_provider; the label is resolved at
  -- render time through /v1/channel-providers.
  --
  -- Deliberately NO FK into channel_provider. The registry never deletes rows
  -- today, which is exactly the kind of invariant that holds until it does not,
  -- and coupling a swept diagnostic table to a registry this module does not own
  -- buys nothing. The grammar is channel_provider.provider's (0247), widened for
  -- the `ext:<unit>:<system>` provenance an extension unit's ingress carries.
  connector      text NOT NULL
                 CHECK (connector ~ '^[a-z][a-z0-9_:.-]*$' AND char_length(connector) <= 96),

  -- The message's natural key, and the ONLY thing the unique index below has to
  -- work with -- activity_id is the link, so nothing reads this to find a row.
  --
  -- ADR-0082 §1 permits a drop to record "the connector, the source system and
  -- the external id", but that was written about MAIL, where the external id is
  -- a message-id. For a channel record it is the customer's provider account id,
  -- which this codebase already treats as personal data: logEnsureFault omits it
  -- for channel records and refuseErasedChannelAccount will not name it at all.
  --
  -- So a channel record's id is stored HASHED (capture.traceSourceID). Dedupe is
  -- equality, and a deterministic hash is equal to itself, so the index is
  -- unaffected -- while an erasure landing inside the 24h window has nothing
  -- here to reach. Mail keeps its message-id, which the ADR permits and which is
  -- what makes a support question answerable.
  source_system  text NOT NULL CHECK (length(source_system) > 0),
  source_id      text NOT NULL CHECK (length(source_id) > 0),

  -- ONE row per message, and these five PARTITION it: a message either never
  -- landed (internal) or landed and its sender was settled one of four ways.
  --
  -- The verdict engine's outcomes are deliberately absent. They are facts about
  -- a SENDER's open question, not about a message, and the disposition ledger
  -- already records them with an owner, a status, a kind and its timestamps. A
  -- copy here would need the sender's question filed under one arbitrary message
  -- of the several it covers, and would collide with itself the moment a sender
  -- were re-judged inside the window. The read joins the ledger instead, on
  -- activity_id, which needs no address and so works with payloads off.
  outcome        text NOT NULL CHECK (outcome IN (
                   'captured','internal','suppressed','deferred','fault')),

  -- A CLASS this installation chose, never a provider's message and never
  -- content: it is rendered on a screen, and a remote party's prose is not this
  -- installation's to display.
  reason         text NULL,

  -- The row this message became, when it became one. Not an FK: the activity
  -- can be erased or retention-swept independently, and a trace that blocked an
  -- erasure would invert its own purpose.
  --
  -- It is also the JOIN to the disposition ledger, which is how a deferred
  -- message shows what later became of its sender without this table storing a
  -- second copy of that answer -- or an address to join on, which it does not
  -- have unless an operator turned payloads on.
  activity_id    uuid NULL,

  -- Layer 2, NULL unless capture.trace_payloads is on (deployconfig, off by
  -- default, settable only in margince.yaml). Bounded on write, normalized
  -- lower-case so the privacy engine's erasure predicate is index-backed
  -- equality rather than a scan.
  --
  -- An ERASED subject's address is never written here whatever the posture says:
  -- Trace consults the same suppression list recordDisposition does, for the
  -- same reason it does -- deletion sticks at the write, and a diagnostic table
  -- is exactly where an erased address would otherwise re-materialize.
  counterparty   text NULL CHECK (counterparty IS NULL OR char_length(counterparty) <= 320),
  subject        text NULL CHECK (subject IS NULL OR char_length(subject) <= 300),

  occurred_at    timestamptz NOT NULL DEFAULT now()
);

-- One row per message per outcome PER MEMBER.
--
-- The internal gate fires BEFORE the dedupe upsert, so without this a re-walked
-- region counts one colleague message once per poll and the funnel measures
-- polling rather than mail.
--
-- user_id is IN the key, and that is not tidiness: the same provider message can
-- reach two connected mailboxes in one workspace, and a key without it would let
-- the first member's row swallow the second's -- whose own view would then omit
-- their own message, which is the single promise this table makes. COALESCE
-- because a NULL never equals a NULL in a unique index, so workspace-owned rows
-- would otherwise not be deduped at all.
CREATE UNIQUE INDEX capture_trace_natural_key ON capture_trace
  (workspace_id, COALESCE(user_id, '00000000-0000-0000-0000-000000000000'::uuid),
   source_system, source_id, outcome);

-- The personal read: one member's window, newest first.
CREATE INDEX capture_trace_user_window
  ON capture_trace (workspace_id, user_id, occurred_at DESC);

-- The workspace read and the sweep both scan by time alone.
CREATE INDEX capture_trace_window
  ON capture_trace (workspace_id, occurred_at DESC);

-- The erasure and subject-access selector. It is partial because the column is
-- NULL for every row unless an operator turned payload capture on, and a
-- deployment that never does should not carry the index at all.
CREATE INDEX capture_trace_counterparty
  ON capture_trace (workspace_id, counterparty)
  WHERE counterparty IS NOT NULL;
