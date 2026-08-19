-- SPDX-License-Identifier: BUSL-1.1
-- SPDX-FileCopyrightText: 2026 Gradion
--
-- One row per member who has connected their own personal Zalo account: which
-- account it is, whether the connection still works, and whether capture is
-- armed. It is applied by the pre-merge gate as a restricted ext_zalo_personal
-- role and at runtime by margince_owner; what isolates it in production is the
-- FORCE row level security and the workspace-bound policy below, not its
-- ownership.
--
-- THE SESSION IS NOT HERE. A member's Zalo login — cookies, device id, user
-- agent — lives as ONE sealed document in the unit's user-scoped secret
-- namespace. This table records only THAT a member connected and which account
-- to, which is also what makes every column here safe for a screen to render.
--
-- And that split matters more in this unit than in its siblings: the sealed
-- credential reads a human's entire personal chat life, so the boundary between
-- "a fact about the connection" and "the credential itself" is the boundary
-- between a row an administrator may look at and one nobody may.

CREATE TABLE ext.ext_zalo_personal_connection (
    id              uuid        NOT NULL DEFAULT gen_random_uuid() PRIMARY KEY,
    -- The tenant claim, and the column the policy below compares. The cascade
    -- is load-bearing: without it, deleting a workspace fails on this unit's
    -- rows, so erasing a tenant would stop at the first installed extension.
    workspace_id    uuid        NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,

    -- WHOSE connection this is: the member who scanned the QR, whose sealed
    -- session every capture and every reply spends, and whose live authority
    -- each of those runs under. Stamped by the handler from the invocation's
    -- Caller and never from the request body — a user id a client supplies is a
    -- user id a client forges, and here it would forge one colleague's consent
    -- to have another read their personal messages.
    --
    -- NO FOREIGN KEY to the core user table: the role this file is applied as
    -- holds REFERENCES (id) on workspace and nothing else on public. The cost
    -- is real and worth stating — nothing deletes this row when the account is
    -- deleted, so a reader must treat the id as one that may no longer resolve.
    user_id         uuid        NOT NULL,

    -- Three states, and no more than this unit can honestly distinguish.
    --
    -- `connected` is working. `needs_reconnect` is a session that stopped being
    -- accepted — evicted by the member's own Zalo Web, or simply expired — and
    -- the only way back is that human re-scanning a QR with their phone, which
    -- is why it is a state a screen shows rather than a fault a retry clears.
    -- `disconnected` is the member having withdrawn: the credential is gone,
    -- and the row stays so their screen can say why capture stopped and so the
    -- next connect updates rather than resurrects.
    status          text        NOT NULL DEFAULT 'connected'
                    CHECK (status IN ('connected', 'needs_reconnect', 'disconnected')),

    -- WHICH Zalo account was scanned, as Zalo's own opaque account id, and it
    -- is NOT NULL because a row exists only once a scan has been confirmed —
    -- there is no half-connected state to leave it empty in.
    --
    -- It is taken from the CREDENTIAL (the resumed session says whose it is)
    -- rather than from anything a client sent, and it is what tells this unit's
    -- own outbound echo apart from an inbound message when capture lands.
    zalo_uid        text        NOT NULL CHECK (length(zalo_uid) > 0),

    -- What the member's own account is called, for their screen to render. It
    -- is nullable because the provider does not always report one, and a name
    -- is never used to route anything.
    display_name    text,

    -- THE CONSENT SWITCH, and the mechanism behind "capture nothing until the
    -- member chooses" rather than a preference on top of it.
    --
    -- FALSE on insert, always: connecting an account is not consenting to have
    -- its conversations read, and the scheduled capture refuses to open a
    -- socket for a row where this is false. It goes back to false when a member
    -- connects a DIFFERENT account, because the list they chose names
    -- counterparties of the account they just replaced.
    capture_enabled boolean     NOT NULL DEFAULT false,

    -- What the last capture did, for the screen and for a human debugging one.
    -- last_error_class is a CLASS, never the provider's own message: it is
    -- rendered, and a provider's error text is a remote party's copy.
    last_polled_at  timestamptz,
    last_error_class text,

    -- When this account was last confirmed by a scan. It is what a screen shows
    -- beside "connected", and what makes a re-scan visible as a re-scan.
    connected_at    timestamptz NOT NULL DEFAULT now(),

    -- Optimistic concurrency for the row, incremented by every update. Two
    -- writers here are ordinary: a member disconnecting while a capture tick is
    -- writing their last-polled time.
    version         integer     NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),

    -- ONE connection per member. Without it a member could scan twice and have
    -- two rows over one account, each capture advancing its own cursor and each
    -- hiding the other's gaps — and a disconnect would leave whichever row it
    -- did not find.
    CONSTRAINT ext_zalo_personal_connection_one_per_member
        UNIQUE (workspace_id, user_id)
);

-- ENABLE and FORCE, both. The owner at runtime is margince_owner, and ENABLE
-- alone exempts a table's owner from its own policies — so without FORCE the
-- isolation would hold for margince_app and not for the role every migration
-- and every operator psql session arrives as.
ALTER TABLE ext.ext_zalo_personal_connection ENABLE ROW LEVEL SECURITY;
ALTER TABLE ext.ext_zalo_personal_connection FORCE ROW LEVEL SECURITY;

-- Exactly one policy, permissive, ALL commands, to PUBLIC. A second permissive
-- policy ORs with this one and can only widen it, and USING without WITH CHECK
-- would admit writes into another workspace.
CREATE POLICY ext_zalo_personal_connection_tenant_isolation
    ON ext.ext_zalo_personal_connection
    USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
    WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);

-- The app role runs the unit's handlers, under the policy above. TRUNCATE is
-- deliberately absent: it empties every tenant's rows without consulting the
-- policy's USING clause.
GRANT SELECT, INSERT, UPDATE, DELETE ON ext.ext_zalo_personal_connection TO margince_app;
