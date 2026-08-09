-- MCP Tasks (io.modelcontextprotocol/tasks): the durable handle a confirm-first
-- tool call hands back while a human decides.
--
-- A task is OPERATIONAL state, not a domain record — the same class as
-- agent_run and the idempotency claim — so it carries no audit or outbox row of
-- its own. The effect a task performs is executed through the normal tool path
-- and carries the full write shape there.
--
-- The row exists for one thing the approval it points at cannot hold: the
-- RESULT. Redemption is single-use, so without a stored terminal state a second
-- poll of a completed task would find the approval consumed and have nothing to
-- answer with, after the first poll had already said "completed".

CREATE TABLE agent_task (
  id             uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id   uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,

  -- The staged decision this handle waits on. NOT cascaded: an approval is an
  -- audit-bearing authority object and nothing deletes one, so a cascade could
  -- only ever hide a defect.
  approval_id    uuid NOT NULL,

  -- The passport the handle belongs to. This — not the id's entropy — is what
  -- makes a task id useless to anyone else: every task method requires the
  -- presenting passport to match, and a mismatch answers what an unknown id
  -- answers.
  passport_id    uuid NOT NULL,

  tool           text NOT NULL,

  -- Only WIRE values. There is no internal "running" state shown to the client
  -- as something else: a second vocabulary is how the two would eventually
  -- disagree. Work in flight is claimed_at, which is a timestamp.
  status         text NOT NULL DEFAULT 'working'
                 CHECK (status IN ('working','completed','failed','cancelled')),
  status_message text NULL,

  -- The tool result a completed task answers with, and the JSON-RPC error a
  -- failed one carries.
  result         jsonb NULL,
  error          jsonb NULL,

  -- Set while one poll is executing the released call, so two simultaneous
  -- polls cannot both run it. A claim older than the executor's lease may be
  -- taken again; that retry is safe because redemption is single-use, and the
  -- re-claim is also the only local evidence that an earlier attempt died
  -- without recording an outcome.
  claimed_at     timestamptz NULL,

  -- When this server stops answering for the handle. It tracks the approval's
  -- own window rather than a constant: a handle outliving the decision it
  -- points at would be polled forever against something nobody can release.
  expires_at     timestamptz NOT NULL,

  created_at     timestamptz NOT NULL DEFAULT now(),
  updated_at     timestamptz NOT NULL DEFAULT now(),

  -- A completed task with no result is a task whose second poll has nothing to
  -- say after its first said "completed". Asserted here rather than trusted to
  -- every writer.
  CONSTRAINT agent_task_terminal_payload CHECK (
    (status = 'completed' AND result IS NOT NULL AND error IS NULL) OR
    (status = 'failed'    AND error  IS NOT NULL AND result IS NULL) OR
    (status IN ('working','cancelled') AND result IS NULL AND error IS NULL)
  ),

  -- COMPOSITE, like every other tenant-local reference: a single-column FK
  -- would accept an approval or a passport from another workspace, and the
  -- application checks above it would then be the only thing standing between
  -- a task and a decision it must never be able to name. The database rejects
  -- it instead.
  CONSTRAINT agent_task_approval_fk FOREIGN KEY (workspace_id, approval_id)
    REFERENCES approval (workspace_id, id) ON DELETE RESTRICT,
  CONSTRAINT agent_task_passport_fk FOREIGN KEY (workspace_id, passport_id)
    REFERENCES passport (workspace_id, id) ON DELETE RESTRICT
);

-- One task per staged approval. Two handles on one decision would let a client
-- poll one to completion and the other into the interrupted answer, for a
-- single human yes.
CREATE UNIQUE INDEX idx_agent_task_approval ON agent_task (workspace_id, approval_id);

-- The retention sweep's access path: expired rows, oldest first.
CREATE INDEX idx_agent_task_expiry ON agent_task (workspace_id, expires_at);

ALTER TABLE agent_task ENABLE ROW LEVEL SECURITY;
ALTER TABLE agent_task FORCE ROW LEVEL SECURITY;

CREATE POLICY agent_task_ws ON agent_task
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);
