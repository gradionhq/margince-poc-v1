-- SPDX-License-Identifier: BUSL-1.1
-- SPDX-FileCopyrightText: 2026 Gradion
--
-- The member's verdicts about their own counterparties, and the cursor the
-- capture that reads them advances. Both are in ONE migration on purpose, and
-- the reason is worth the paragraph: the verdict table is what ARMS capture,
-- and the cursor is what stops an armed capture re-landing a member's whole
-- undelivered backlog on every tick. Shipped as two versions there is a state a
-- database can legitimately sit in — verdicts present, cursor absent — where the
-- first save turns on a capture the schema cannot bookmark. They are two halves
-- of one capability and they land together.
--
-- TWO MODES, ONE TABLE. A member either captures everything except the people they
-- leave out, or only the people they choose — and this table holds both lists: the
-- `block` rows are the exclusion list of the first mode and the `allow` rows are the
-- inclusion list of the second. Which list is consulted is the connection's
-- capture_mode, added at the bottom of this file.
--
-- WHAT THE VERDICT TABLE IS FOR, said plainly because it is the whole consent
-- story of this unit: a personal Zalo account has no business/private split, so
-- the credential a member deposits reaches their family, their doctor and their
-- other employer alongside their customers. This table is where that member —
-- not an administrator, not this installation — says which conversations the CRM
-- may read. The default is capture NOTHING: a counterparty with no row here is
-- dropped at the wire, so an unallowed conversation never becomes a row somebody
-- later has to delete.

CREATE TABLE ext.ext_zalo_personal_allowlist (
    id              uuid        NOT NULL DEFAULT gen_random_uuid() PRIMARY KEY,
    -- The tenant claim, and the column the policy below compares. The cascade is
    -- load-bearing for the same reason it is on the connection table: without it
    -- erasing a workspace stops at the first installed extension.
    workspace_id    uuid        NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,

    -- WHOSE verdict this is. A rep configures their own list and nobody
    -- configures it for them, so this is stamped by the handler from the
    -- invocation's Caller and never from the request body — a user id a client
    -- supplies is a user id a client forges, and here it would forge one
    -- colleague's decision about who may read their private conversations.
    --
    -- NO FOREIGN KEY to the core user table, as on the connection table: the
    -- role this file is applied as holds REFERENCES on workspace and nothing
    -- else on public. A reader must treat the id as one that may no longer
    -- resolve.
    user_id         uuid        NOT NULL,

    -- WHICH counterparty, as Zalo's own opaque account id for them. It is the
    -- id the drained frame carries in uidFrom, which is what lets a verdict be
    -- matched against a message without trusting a display name.
    channel_user_id text        NOT NULL CHECK (length(channel_user_id) > 0),

    -- The verdict, and which of the two modes it speaks to. `allow` is a row of
    -- the inclusion list, read only under only_chosen; `block` is a row of the
    -- exclusion list, read only under everyone_except. A row is therefore inert in
    -- the other mode rather than wrong in it — which is why switching modes needs
    -- no rewrite of this table.
    --
    -- Two values and no third. Absence means something different in each mode
    -- (not included, or not excluded), and that is exactly what the mode is for:
    -- one table, two readings, no third state to invent.
    mode            text        NOT NULL
                    CHECK (mode IN ('allow', 'block')),

    -- What the member's screen calls this counterparty, kept so a list still
    -- reads as people after the provider stops answering a roster call. It is
    -- nullable because the provider does not always report one, and it is never
    -- used to match a message — matching is on the account id above.
    display_name    text,

    -- Optimistic concurrency for the row, incremented by every update. It is
    -- ordinary here: a member saving their list on a phone while the same list
    -- is open on a laptop.
    version         integer     NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),

    -- ONE verdict per member per counterparty. Without it a save could leave
    -- an `allow` and a `block` for the same person side by side, and the filter
    -- would decide by whichever row it read first — which is a consent decision
    -- made by a query plan.
    CONSTRAINT ext_zalo_personal_allowlist_one_verdict_per_counterparty
        UNIQUE (workspace_id, user_id, channel_user_id)
);

-- The filter reads one member's whole list on every tick, so the ordering pair
-- the read uses is the index. Without it a busy installation's tick table-scans
-- every member's verdicts once per member per cadence.
CREATE INDEX ext_zalo_personal_allowlist_by_member
    ON ext.ext_zalo_personal_allowlist (workspace_id, user_id, channel_user_id);

-- ENABLE and FORCE, both. The owner at runtime is margince_owner, and ENABLE
-- alone exempts a table's owner from its own policies — so without FORCE the
-- isolation would hold for margince_app and not for the role every migration
-- and every operator psql session arrives as.
ALTER TABLE ext.ext_zalo_personal_allowlist ENABLE ROW LEVEL SECURITY;
ALTER TABLE ext.ext_zalo_personal_allowlist FORCE ROW LEVEL SECURITY;

-- Exactly one policy, permissive, ALL commands, to PUBLIC. A second permissive
-- policy ORs with this one and can only widen it, and USING without WITH CHECK
-- would admit writes into another workspace.
CREATE POLICY ext_zalo_personal_allowlist_tenant_isolation
    ON ext.ext_zalo_personal_allowlist
    USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
    WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);

-- The app role runs the unit's handlers, under the policy above. TRUNCATE is
-- deliberately absent: it empties every tenant's rows without consulting the
-- policy's USING clause.
GRANT SELECT, INSERT, UPDATE, DELETE ON ext.ext_zalo_personal_allowlist TO margince_app;

-- WHICH OF THE TWO MODES THIS MEMBER CHOSE, and when.
--
-- NULLABLE, and that is the honest encoding of "they have not decided yet" rather
-- than a defaulted mode that would put words in their mouth. There is no third
-- enum value for "unset" because capture_enabled already carries that fact, and two
-- columns saying the same thing is two columns that can disagree.
--
-- CHOOSING everyone_except IS AN INFORMED ACT BY THE MEMBER, NEVER A DEFAULT. It is
-- the line between consent and an accident: a default of "capture everything" would
-- mean an installation reading a human's entire personal chat life because nobody
-- touched a screen. So the default is no mode at all, capture_enabled stays false
-- until the member saves one, and the CHECK below makes "armed with no mode" a state
-- the database refuses to hold.
ALTER TABLE ext.ext_zalo_personal_connection
    ADD COLUMN capture_mode text
        CHECK (capture_mode IN ('everyone_except', 'only_chosen')),

    -- WHEN THIS MODE WAS CHOSEN, which is a floor and not a decoration: under
    -- everyone_except a conversation nobody has ever mentioned is captured from
    -- HERE forward rather than back through whatever Zalo is still holding. The
    -- reasoning is in record.go, on the filter that reads it.
    --
    -- It moves only when the mode CHANGES, never on an ordinary edit of the lists —
    -- otherwise re-saving an exclusion list would silently move the floor forward
    -- and lose the messages between the two saves.
    ADD COLUMN capture_mode_since timestamptz,

    -- CAPTURE CANNOT BE ARMED WITHOUT A MODE. Without this the pair has a fourth
    -- state — armed, mode NULL — in which the filter has no rule to apply, and the
    -- only safe behaviour for code that reaches it is to capture nothing, which
    -- looks exactly like a broken connector. The database refusing it means that
    -- state cannot be reached by any writer, including a future one.
    ADD CONSTRAINT ext_zalo_personal_connection_armed_has_a_mode
        CHECK (capture_enabled = false OR capture_mode IS NOT NULL);
