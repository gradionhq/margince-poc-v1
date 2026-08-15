-- 0260: a scheduled message remembers WHICH agent scheduled it.
--
-- The row already stores the authorizing human (`scheduled_by`) and whether the
-- actor was a human or an agent (`principal_kind`). That is enough to fire with
-- the right sign-off — an agent-scheduled message must not go out over the
-- human's signature — but not enough to say WHO acted.
--
-- At fire the worker rebuilt an agent as `agent:<the human's id>`: an identity
-- no agent ever had. Two agents, or two passports, acting for the same human
-- collapsed into one invented actor, so the release audit row, the activity's
-- captured_by and the outbox envelope could not name which agent produced the
-- message. That is the attribution chain ADR-0055 rests on, broken exactly
-- where an incident investigation looks.
--
-- These three columns are the SCHEDULING-TIME record, and they are immutable
-- once written. They are deliberately not the live authority: the fire path
-- still re-reads the human's seat and grants, and that stays the ceiling (a
-- passport revoked in between must still stop the send). Stored provenance
-- answers "who asked for this", live authority answers "may it still go" — two
-- different questions that were being answered by one field.
--
-- All three are NULL for a human-scheduled message, and NULL for the rows that
-- already exist. A backfill would have to invent the very identity this
-- migration exists to stop inventing: a pre-existing agent-scheduled row cannot
-- say which agent it was, and NULL is the honest record of that. The fire path
-- reads it as "no stored provenance" and falls back to the pre-0260 behaviour
-- for those rows only.
ALTER TABLE scheduled_send
  ADD COLUMN agent_actor_id  text NULL,
  ADD COLUMN agent_passport_id uuid NULL,
  ADD COLUMN agent_on_behalf_of uuid NULL REFERENCES app_user(id) ON DELETE SET NULL;

-- agent_actor_id is the anchor: it is what "which agent was this" means, and it
-- is the one fact the fire path cannot reconstruct. The other two hang off it.
--
-- The passport and the human are independently optional, and each NULL means
-- something specific rather than "missing":
--
--   * no passport — the action was not taken under one (an in-process agent).
--   * no human — the actor named none. That is a real shape: an agent
--     principal whose UserID is an AGENT's own app_user row carries no
--     OnBehalfOf, and copying the UserID in would put an agent's id in a column
--     meaning "the human behind this", which auth.Admit reads to derive seat
--     and RBAC.
--
-- What the constraint forbids is provenance with no actor: a passport or a
-- human hanging off a row that cannot say which agent they belong to, and any
-- agent provenance at all on a human-scheduled message.
--
-- The all-NULL arm is only for the rows that already exist. An agent-scheduled
-- row written before this migration has no provenance and cannot be given one,
-- so the fire path reads a NULL actor id as the pre-0260 row it is. A row
-- written AFTER this migration always names its agent — which is what keeps
-- that reading unambiguous, and why agent_actor_id is required whenever any
-- provenance is present rather than the three travelling as one block.
ALTER TABLE scheduled_send ADD CONSTRAINT scheduled_send_agent_provenance_shape CHECK (
  (principal_kind = 'agent' AND agent_actor_id IS NOT NULL)
  OR
  (agent_actor_id IS NULL AND agent_passport_id IS NULL AND agent_on_behalf_of IS NULL)
);
