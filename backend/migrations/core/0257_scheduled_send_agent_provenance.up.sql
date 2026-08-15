-- 0257: a scheduled message remembers WHICH agent scheduled it.
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
-- reads it as "no stored provenance" and falls back to the pre-0257 behaviour
-- for those rows only.
ALTER TABLE scheduled_send
  ADD COLUMN agent_actor_id  text NULL,
  ADD COLUMN agent_passport_id uuid NULL,
  ADD COLUMN agent_on_behalf_of uuid NULL REFERENCES app_user(id) ON DELETE SET NULL;

-- The three travel together or not at all. A row carrying a passport but no
-- actor id, or agent provenance on a human-scheduled message, is a writer bug
-- and the schema is where it stops rather than becoming a mis-attributed audit
-- row somebody trusts later.
--
-- The all-NULL arm is what lets the rows that already exist stay valid: an
-- agent-scheduled row written before this migration has no provenance and
-- cannot be given one. So this constraint does NOT say "every agent row names
-- its agent" — it says a row either names it completely or not at all. The
-- writer is what makes new agent rows complete, and the fire path treats a
-- NULL actor id as the pre-0257 row it is.
ALTER TABLE scheduled_send ADD CONSTRAINT scheduled_send_agent_provenance_shape CHECK (
  (principal_kind = 'agent' AND agent_actor_id IS NOT NULL AND agent_on_behalf_of IS NOT NULL)
  OR
  (agent_actor_id IS NULL AND agent_passport_id IS NULL AND agent_on_behalf_of IS NULL)
);
