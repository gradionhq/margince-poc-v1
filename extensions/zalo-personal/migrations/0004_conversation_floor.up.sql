-- SPDX-License-Identifier: BUSL-1.1
-- SPDX-FileCopyrightText: 2026 Gradion
--
-- WHEN A MEMBER'S DECISION ABOUT ONE CONVERSATION LAST NARROWED — the durable mark
-- an EXPLICIT exclusion leaves behind after the exclusion itself is gone.
--
-- THE DEFECT THIS EXISTS FOR. Under everyone_except a member leaves one person out.
-- That person's messages arrive for a week and are correctly refused, so nothing
-- advances: no record lands, no bookmark moves. The member then takes them off the
-- leave-out list — and the whole blocked week, still inside Zalo's retention window,
-- lands on the next tick. Nothing in the schema before this file could prevent it:
-- capture_mode_since is from when the MODE was chosen, which was before the block;
-- the cursor row's updated_at is from before the block too; and the verdict row that
-- recorded the block is DELETED by the act of lifting it, so the one timestamp that
-- would have said "up to here you had said no" is destroyed at exactly the moment it
-- becomes load-bearing.
--
-- THE RULE IS ASYMMETRIC, and the asymmetry is the whole design:
--
--   * LIFTING AN EXCLUSION DOES NOT RETROACTIVELY ADMIT THE EXCLUDED PERIOD.
--     "Unblock Alice" means "capture Alice from now", never "capture the week I was
--     hiding". So the instant an exclusion is lifted becomes that conversation's
--     floor, and this row is where that instant lives.
--   * NAMING SOMEBODY INTO CAPTURE STILL GIVES THEM THEIR BACKLOG. Under
--     only_chosen, adding somebody who was never excluded lands everything Zalo is
--     still holding for them — which is the promise the per-conversation cursor was
--     introduced to keep, and which this table does not touch, because that
--     conversation has no row here.
--
-- The principle both arms come out of: AN EXPLICIT EXCLUSION LEAVES A MARK; A
-- CONVERSATION THAT WAS NEVER EXCLUDED CARRIES NONE.
--
-- WHY ITS OWN TABLE, and what was rejected. The full argument is in floor.go, beside
-- the code that reads it; the short form is that the two cheaper homes are each
-- wrong for a reason the schema states about itself:
--
--   * A COLUMN ON THE VERDICT ROW cannot work, because lifting the exclusion is
--     exactly the act that DELETES that row. Keeping a tombstone instead would put a
--     row the member asked to be removed back into the list their own screen reads,
--     and every reader of that list would then need to know which rows are consent
--     and which are residue.
--   * A COLUMN ON THE CURSOR ROW would put a consent fact inside the table this
--     unit's own migration 0003 declares to be scheduling state — "a bookmark is
--     scheduling state and losing one costs a deduplicated replay", said there to
--     justify that it may be pruned. A floor may not be lost: losing one hands over
--     the excluded period. And a cursor row is NOT NULL on last_msg_id with a
--     numeric CHECK, so a floor for a conversation nothing ever landed from would
--     have to invent a sentinel bookmark — which the filter reads as evidence that
--     capture has been reading that conversation, silencing the very floor being
--     written.
--
-- A SEPARATE VERSION FROM 0003 rather than an edit of it. 0003 is unshipped except on
-- this branch, but it is APPLIED on a developer database here, and an applied version
-- never re-runs: editing it would change what a fresh installation gets while that
-- database silently kept the old schema. The two halves would diverge with nothing to
-- show it.
--
-- GROWTH IS BOUNDED BY EXCLUSIONS LIFTED, not by messages or even by people: one row
-- per counterparty a member has ever explicitly excluded and then stopped excluding.
-- Nothing sweeps it, and nothing may: a floor is consent and it has to outlive every
-- message it is a statement about.

CREATE TABLE ext.ext_zalo_personal_conversation_floor (
    -- An id of its own, unlike the cursor table's composite key, because this row is
    -- RECORDED: the ledger's entity_id is a uuid, and a fact about consent that
    -- cannot be recorded is one nobody can answer a subject request with.
    id              uuid        NOT NULL DEFAULT gen_random_uuid() PRIMARY KEY,

    -- The tenant claim, and the column the policy below compares. The cascade is
    -- load-bearing: without it, deleting a workspace fails on this unit's rows.
    workspace_id    uuid        NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,

    -- WHOSE decision this records. Stamped by the handler from the invocation's own
    -- member, exactly as the verdict it is derived from: a forged one here would let
    -- a caller move the floor on a colleague's conversation, which either hands over
    -- a period that member had hidden or silently stops capturing one they allowed.
    user_id         uuid        NOT NULL,

    -- WHICH conversation, as the counterparty's own Zalo account id — the same value
    -- a verdict and a cursor are keyed on, and deliberately NOT a foreign key to a
    -- verdict: this row EXISTS BECAUSE the verdict does not any more.
    channel_user_id text        NOT NULL CHECK (length(channel_user_id) > 0),

    -- NOTHING BEFORE THIS INSTANT. It is when the last explicit exclusion of this
    -- conversation was lifted, written from the DATABASE's own now() in the same
    -- transaction as the verdict change that lifted it — never from the
    -- application's clock, which is a different clock from the one every other
    -- timestamp this filter compares against.
    --
    -- It only ever moves FORWARD, because every write of it is now() and now() is
    -- later than whatever was here. A floor that could move backwards would re-open
    -- a period the member had already closed.
    not_before      timestamptz NOT NULL,

    -- Optimistic concurrency, and here it is also what tells a first exclusion from a
    -- second: the ledger records a create or an update accordingly.
    version         integer     NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),

    -- ONE floor per conversation per member per tenant. Two rows would let the filter
    -- pick a floor by whichever the query plan read first, which is a consent
    -- decision made by the planner. It is also the index the tick's read uses, which
    -- asks for one member's whole set.
    CONSTRAINT ext_zalo_personal_conversation_floor_one_per_counterparty
        UNIQUE (workspace_id, user_id, channel_user_id)
);

-- ENABLE and FORCE, both. The owner at runtime is margince_owner, and ENABLE alone
-- exempts a table's owner from its own policies — so without FORCE the isolation
-- would hold for margince_app and not for the role every migration and every
-- operator psql session arrives as.
ALTER TABLE ext.ext_zalo_personal_conversation_floor ENABLE ROW LEVEL SECURITY;
ALTER TABLE ext.ext_zalo_personal_conversation_floor FORCE ROW LEVEL SECURITY;

-- Exactly one policy, permissive, ALL commands, to PUBLIC. A second permissive
-- policy ORs with this one and can only widen it, and USING without WITH CHECK
-- would admit writes into another workspace.
CREATE POLICY ext_zalo_personal_conversation_floor_isolation
    ON ext.ext_zalo_personal_conversation_floor
    USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
    WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);

-- The app role runs the unit's handlers, under the policy above. TRUNCATE is
-- deliberately absent: it empties every tenant's rows without consulting the
-- policy's USING clause.
--
-- DELETE IS PRESENT, and it is the uniform grant every tenant table in this
-- schema carries — the migration gate requires exactly it, so that a table can
-- never answer `permission denied` to the runtime role that owns it. That a
-- floor is never removed is true and load-bearing (it is the record of a period
-- a member excluded), but it is a fact about what this unit DOES, not about what
-- the role MAY do, and it is kept where it can be read and enforced:
-- TestNothingInThisUnitEverDeletesAFloor.
GRANT SELECT, INSERT, UPDATE, DELETE ON ext.ext_zalo_personal_conversation_floor TO margince_app;
