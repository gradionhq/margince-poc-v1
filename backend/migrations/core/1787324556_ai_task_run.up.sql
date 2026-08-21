-- The AI-activity projection: one row per AI task OCCURRENCE, fed from the bus
-- by cg:ai-activity and read by the rail and (later) the activity feed.
--
-- Derived read-model state, so no audit or outbox ride-along of its own — the
-- same class as the brief read model. The events that FEED it carry the write
-- shape at their own writers, which is where the obligation belongs.
--
-- No workspace_id and no RLS: the shape of every core table added since the
-- tenant-column sweep, and 0217 retired row-level security in core. The read's
-- scope is the caller's own user id, imposed per statement.

CREATE SEQUENCE ai_task_run_seq;

-- The projection's own monotonic clock. Never an emitter's: an emitter
-- timestamp is time.Now() in whichever process emitted, so clock skew between
-- two rival attempts would decide which one's started_at the row kept. seq is
-- also the feed's keyset cursor and the SSE Last-Event-ID, which is why one
-- column serves all three.
COMMENT ON SEQUENCE ai_task_run_seq IS
  'Monotonic ordering for ai_task_run: the delta cursor and the stream id.';

-- Rank within one attempt. IMMUTABLE so the guard's tuple comparison can use it
-- in an index-friendly predicate. A settled state outranks running, which is
-- what makes settled terminal WITHIN an attempt; a higher attempt outranks
-- everything, which is what lets a re-queue legitimately reopen the row.
CREATE FUNCTION ai_task_run_state_rank(state text) RETURNS integer
  LANGUAGE sql IMMUTABLE STRICT AS $$
    SELECT CASE state
      WHEN 'queued'  THEN 0
      WHEN 'running' THEN 1
      ELSE 2
    END
  $$;

CREATE TABLE ai_task_run (
  id             uuid PRIMARY KEY DEFAULT uuidv7(),

  -- Identity is the SOURCE's; display is the contract's. Split because they
  -- change for different reasons, and because two sources must never collide
  -- on one occurrence key.
  source         text NOT NULL,
  occurrence_key text NOT NULL,
  kind           text NOT NULL,

  -- The api/ai-tasks.yaml task that did the model work, when one did. Held to
  -- the generated task set by a root-package fitness test: a module may not
  -- import the ai module to assert this itself.
  ai_task        text NULL,

  -- Which attempt this state belongs to. The lifecycle is NOT monotonic: a
  -- claimed reading can be released or re-armed back to queued and claimed
  -- again. state alone cannot order two events.
  attempt        integer NOT NULL DEFAULT 1 CHECK (attempt >= 1),

  -- Two independent facts. 'personal' with a NULL user means the person is
  -- GONE — the occurrence stays as history and is shown to nobody. 'workspace'
  -- means it belonged to nobody from the start. Conflating them would relabel a
  -- leaver's work as a system sweep.
  actor_scope    text NOT NULL CHECK (actor_scope IN ('personal','workspace')),
  actor_user_id  uuid NULL REFERENCES app_user(id) ON DELETE SET NULL,
  passport_id    uuid NULL REFERENCES passport(id) ON DELETE SET NULL,

  state          text NOT NULL CHECK (state IN ('queued','running','done','degraded','failed')),

  -- When the CURRENT attempt was enqueued, which is what a live row ages from
  -- and what the live feed orders by. Deliberately not the occurrence's first
  -- enqueue: a re-queued occurrence dated by its original creation is past its
  -- lease before any worker sees it.
  queued_at      timestamptz NOT NULL,
  started_at     timestamptz NULL,
  finished_at    timestamptz NULL,

  -- The source's own lease on a live attempt. The read renders a live row past
  -- this as stalled, so a worker that died without emitting anything cannot be
  -- displayed as working.
  stale_after    timestamptz NULL,

  -- Written from the capture-activity slice; present now so that slice needs no
  -- reshaping migration. A NULL subject renders no name, which is what keeps row
  -- scope on subjects out of scope rather than unhandled.
  subject_type   text NULL,
  subject_id     uuid NULL,
  quantity       integer NULL CHECK (quantity IS NULL OR quantity >= 0),
  quantity_unit  text NULL CHECK (quantity_unit IS NULL OR
                   quantity_unit IN ('messages','records','people','documents')),

  -- SERVER-AUTHORED prose, never a provider message and never err.Error(): a
  -- provider's own text can echo key material or a prompt injection's payload,
  -- and this column reaches an ordinary rep. The property is about where the
  -- string CAME FROM, not what shape it is.
  degrade_reason text NULL,
  summary        text NULL,

  seq            bigint NOT NULL DEFAULT nextval('ai_task_run_seq'),
  last_event_id  uuid NOT NULL,

  created_at     timestamptz NOT NULL DEFAULT now(),
  updated_at     timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT ai_task_run_identity UNIQUE (source, occurrence_key),
  CONSTRAINT ai_task_run_seq_unique UNIQUE (seq),

  -- A settled row has a finish and a live one does not. Without this an emitter
  -- bug puts a NULL into the settled feed's ORDER BY and the keyset silently
  -- stops paginating.
  CONSTRAINT ai_task_run_settled_has_finish CHECK (
    (state IN ('done','degraded','failed')) = (finished_at IS NOT NULL)),
  CONSTRAINT ai_task_run_queued_has_no_start CHECK (
    (state = 'queued') = (started_at IS NULL)),
  CONSTRAINT ai_task_run_workspace_scope_has_no_actor CHECK (
    actor_scope = 'personal' OR actor_user_id IS NULL)
);

-- Live rows for one person. queued IS live — the rail's running section
-- includes queued occurrences, and an index that omitted them would be missed
-- only under load.
CREATE INDEX ai_task_run_live ON ai_task_run (actor_user_id, queued_at DESC, id DESC)
  WHERE state IN ('queued','running');

-- Settled rows for one person, keyset-ordered. finished_at is not unique, so id
-- is part of the key: without it a page boundary can drop or repeat a row.
CREATE INDEX ai_task_run_settled ON ai_task_run (actor_user_id, finished_at DESC, id DESC)
  WHERE state IN ('done','degraded','failed');

-- The delta cursor: everything that changed for one person since a seq.
CREATE INDEX ai_task_run_cursor ON ai_task_run (actor_user_id, seq DESC);

CREATE TRIGGER trg_ai_task_run_updated BEFORE UPDATE ON ai_task_run
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- The app role needs the SEQUENCE explicitly, and this is the only table in
-- core that does.
--
-- Every other monotonic column in the tree — event_outbox.seq among them — is a
-- GENERATED AS IDENTITY column, and Postgres checks an identity column's
-- privilege on the TABLE, never on the implicit sequence behind it. This one
-- cannot be an identity column: the projection BUMPS seq inside an ON CONFLICT
-- DO UPDATE, which is an ordinary nextval() call and needs USAGE like any
-- other. 0015's ALTER DEFAULT PRIVILEGES covers TABLES only, so without this
-- grant the app role inserts the row and is refused the sequence.
--
-- Conditional for the same reason 0015's block is: a throwaway test database
-- runs everything as the owner and has no margince_app role at all.
DO $$
BEGIN
  IF EXISTS (SELECT FROM pg_roles WHERE rolname = 'margince_app') THEN
    GRANT USAGE, SELECT ON SEQUENCE ai_task_run_seq TO margince_app;
  END IF;
END $$;
