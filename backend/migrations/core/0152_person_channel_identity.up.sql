-- person_channel_identity (telegram-oa design §4.2, owned by people):
-- Telegram (and future channel) identity binding for Person.
--
-- Telegram user ids are GLOBAL, not per-bot — unlike Messenger PSIDs or Zalo
-- ids, which are app-scoped. The key below is right *because* they are
-- global: it omits channel_id, so replacing the workspace's bot preserves
-- every identity binding. The suppression hash key inherits this — it
-- hashes provider:channel_user_id, never the bot id.
--
-- blocked_at is reachability, not identity (D9). The dedupe lane and the
-- unique index both read WHERE archived_at IS NULL; archiving on block would
-- mean that when the user unblocks and writes again — routine — the lane
-- misses and the resolver creates a SECOND Person for the same human, which
-- the partial index happily admits. blocked_at is set and cleared by
-- my_chat_member; reachability reads it; the dedupe lane ignores it.
--
-- Not person_social (0051: workspace/person/platform/handle) — that table
-- has no cross-person uniqueness, no archived_at, and no provenance columns.
-- It is display data, not a resolution key.

CREATE TABLE person_channel_identity (
  id              uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id    uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  person_id       uuid NOT NULL,
  provider        text NOT NULL CHECK (provider IN ('telegram')),
  channel_user_id text NOT NULL,   -- the sender's numeric Telegram id
  username        text NULL,       -- display only, refreshed on every inbound message
  blocked_at      timestamptz NULL,

  -- The membership watermark: which my_chat_member update last decided
  -- blocked_at. Telegram numbers its updates, but the ingest queue runs
  -- several workers, so a block and the unblock answering it can commit either
  -- way round — and the wrong order leaves a reachable customer suppressed for
  -- good, since nothing else ever writes blocked_at.
  --
  -- blocked_at cannot serve as its own ordering evidence: the unblock arm sets
  -- it to NULL and destroys it. The bot is part of the key because update_id is
  -- a PER-BOT sequence — a replacement bot starts low, and an unscoped
  -- watermark would read every one of its updates as stale and wedge the
  -- identity's reachability permanently.
  membership_bot_id    text NULL,
  membership_update_id bigint NULL,

  source          text NOT NULL,
  captured_by     text NOT NULL,
  version         bigint NOT NULL DEFAULT 1,
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now(),
  archived_at     timestamptz NULL,

  -- Composite tenant FK (0019 pattern): a plain person_id -> person(id) FK
  -- would let a satellite point at another workspace's Person.
  CONSTRAINT person_channel_identity_person_id_fkey FOREIGN KEY (workspace_id, person_id)
    REFERENCES person (workspace_id, id) ON DELETE CASCADE
);

CREATE UNIQUE INDEX uq_person_channel_identity
  ON person_channel_identity (workspace_id, provider, channel_user_id) WHERE archived_at IS NULL;

-- The person's channel-identity read path, and what the person_id cascade
-- delete above needs to avoid a sequential scan per deleted Person.
--
-- Unconditional, unlike the unique key above, because a PARTIAL index cannot
-- serve a foreign key: the planner has no proof that a row about to be cascaded
-- satisfies the predicate. Carrying the archived rows too costs one index entry
-- per retired binding and lets ONE index answer both readers.
CREATE INDEX idx_person_channel_identity_person
  ON person_channel_identity (workspace_id, person_id);

CREATE TRIGGER trg_person_channel_identity_updated BEFORE UPDATE ON person_channel_identity
  FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();

ALTER TABLE person_channel_identity ENABLE ROW LEVEL SECURITY;
ALTER TABLE person_channel_identity FORCE ROW LEVEL SECURITY;
CREATE POLICY person_channel_identity_tenant_isolation ON person_channel_identity
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);
