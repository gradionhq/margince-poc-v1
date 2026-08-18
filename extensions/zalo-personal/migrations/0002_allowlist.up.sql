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

    -- The verdict. Two values and no third, because a third would be a state
    -- the filter has no rule for: absence already means "not allowed", so
    -- `block` is a REFINEMENT the member can state deliberately rather than the
    -- mechanism. What makes the mechanism the default is that this table starts
    -- empty.
    mode            text        NOT NULL
                    CHECK (mode IN ('allow', 'block')),

    -- What the member's screen calls this counterparty, kept so a list still
    -- reads as people after the provider stops answering a roster call. It is
    -- nullable because the provider does not always report one, and it is never
    -- used to match a message — matching is on the account id above.
    display_name    text,

    -- THE CURSOR, and it is HERE — on the verdict — rather than on the connection,
    -- which is the one placement that makes the promise this table exists for true.
    --
    -- It holds the highest msgId ingested FOR THIS COUNTERPARTY, and it advances
    -- only after capture has answered. That asymmetry is the safety argument and it
    -- is sound because capture is idempotent on the record's natural key: a cursor
    -- not advanced past a message that landed costs one deduplicated retry, while a
    -- cursor advanced past one that did not land costs the message.
    --
    -- WHY NOT ONE CURSOR PER MEMBER, which is where this started. A single
    -- high-water mark is a MAXIMUM over every conversation, so a message that lands
    -- from an allowed counterparty buries every lower-numbered message that was
    -- dropped from a conversation the member had not chosen. The member then allows
    -- that conversation while Zalo is still holding those messages, and they are
    -- filtered as already-landed: the newly allowed conversation starts EMPTY,
    -- which is exactly what the allowlist is supposed to prevent and exactly what
    -- the plan promises a member who arms a conversation.
    --
    -- Per-counterparty is also SELF-CLEANING, which is why it beats the other
    -- candidate fix (a per-member low-water mark advanced only past a contiguous
    -- run). A counterparty the member has just allowed has NO cursor, so everything
    -- of theirs still in the queue passes — the promise holds by construction
    -- rather than by argument. A block→allow keeps only a cursor earned during an
    -- earlier allow period, and the messages during the block were correctly never
    -- captured. And no conversation can stall another: a busy blocked chat cannot
    -- freeze an allowed one's cursor, which the low-water mark would have let it do
    -- for a whole retention window.
    --
    -- TEXT rather than a number because it is the PROVIDER's identifier and this
    -- unit does not own its shape. The comparison that decides "already landed" is
    -- numeric and is made in Go, where a value that will not parse is refused
    -- BEFORE it can be stored here — a non-numeric cursor would sit above and below
    -- nothing and re-offer every message forever.
    last_msg_id     text        CHECK (last_msg_id IS NULL OR last_msg_id ~ '^[0-9]+$'),

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
