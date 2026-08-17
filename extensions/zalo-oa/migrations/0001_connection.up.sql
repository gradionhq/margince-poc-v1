-- SPDX-License-Identifier: BUSL-1.1
-- SPDX-FileCopyrightText: 2026 Gradion
--
-- ONE row per workspace: which Official Account this installation captures from,
-- which admin authorized it, what the provider says about the account's package,
-- and how far the poll has read. It is applied by the pre-merge gate as a
-- restricted ext_zalo_oa role and at runtime by margince_owner; what isolates it
-- in production is the FORCE row level security and the workspace-bound policy
-- below, not its ownership.
--
-- THE TOKENS ARE NOT HERE. The access and refresh tokens live as one sealed
-- document in the unit's user-scoped secret namespace, under the admin who
-- authorized them. This table records only THAT an account is connected and who
-- stands behind it — which is also what makes every column safe for the screen
-- to render.

CREATE TABLE ext.ext_zalo_oa_connection (
    id              uuid        NOT NULL DEFAULT gen_random_uuid() PRIMARY KEY,
    -- The tenant claim, and the column the policy below compares. The cascade is
    -- load-bearing: without it, deleting a workspace fails on this unit's rows,
    -- so erasing a tenant would stop at the first installed extension.
    workspace_id    uuid        NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,

    -- WHICH Official Account, as Zalo's own id. It is NULL only between starting
    -- an authorization and the admin coming back from the browser with it: the
    -- id arrives on the redirect, not before.
    --
    -- It is also a NAMESPACE. Zalo account ids are OA-scoped rather than global,
    -- so every person binding and every natural key this unit writes is prefixed
    -- with this value; repointing the row at another OA under the same
    -- identifiers would silently rebind every captured human.
    oa_id           text        CHECK (oa_id IS NULL OR length(oa_id) > 0),

    -- The developer app the authorization was made through. Its SECRET is
    -- sealed, not here; the id is not a credential and is needed to build the
    -- permission URL and every later rotation.
    app_id          text        NOT NULL CHECK (length(app_id) > 0),

    -- Where Zalo sends the admin's browser back to. Stored because the exchange
    -- has to present the same value the permission URL was built with, and a
    -- deployment's own address is not something this unit can derive.
    redirect_uri    text        NOT NULL CHECK (length(redirect_uri) > 0),

    -- WHOSE grant this is: the admin who clicked *Cho phép*, whose sealed token
    -- the poll spends, and whose LIVE authority every landed record runs under.
    -- Stamped by the handler from the invocation's Caller and never from the
    -- request body — a user id a client supplies is a user id a client forges,
    -- and here it would forge the consent the ingress port checks.
    --
    -- It is an explicit column rather than an implicit "whoever clicked" because
    -- an OA token is renewable only by the human Zalo bound it to: when they
    -- leave, this connection stops, and a row that could not name them would
    -- make that look like a provider outage.
    --
    -- NO FOREIGN KEY to the core user table: the role this file is applied as
    -- holds REFERENCES (id) on workspace and nothing else on public. The cost is
    -- real and worth stating — nothing deletes this row when the account is
    -- deleted, so a reader must treat the id as one that may no longer resolve.
    -- The poll does: identity refuses a member who is gone, and the row parks
    -- rather than pretending.
    authorized_by   uuid        NOT NULL,

    -- Four states, and no more than this unit can honestly distinguish.
    --
    -- `pending_authorization` is the row between minting a permission URL and the
    -- admin returning with a code — it holds no token and polls nothing.
    -- `connected` is working. `reauth_required` means the credential can no
    -- longer be renewed and a human must authorize again. `tier_lapsed` is the
    -- package expiring under a working connection: the token is fine, the
    -- account's plan is not, and telling an operator to re-authorize would send
    -- them to fix the one thing that is not broken.
    --
    -- A disconnected installation has no row at all (the disconnect deletes it,
    -- with the credential), so there is no state meaning "here but not here".
    status          text        NOT NULL DEFAULT 'pending_authorization'
                    CHECK (status IN ('pending_authorization', 'connected',
                                      'reauth_required', 'tier_lapsed')),

    -- What the provider says about the account, refreshed on every poll: the
    -- OA's name, and the tier EVIDENCE an admin reads.
    --
    -- package_name is a localized Vietnamese display string and is deliberately
    -- never compared against anything — the tier decision is a capability probe,
    -- and a connector that matched on this name would refuse a paying customer
    -- the day the package was renamed. It is stored so a screen can show what the
    -- account is on and until when.
    account_label   text,
    package_name    text,
    package_valid_through text,

    -- When the sealed access token stops being accepted, mirrored out of the
    -- credential so a screen and a pre-flight can answer "is this usable" without
    -- unsealing anything. The token itself is not here and cannot be derived
    -- from this.
    access_token_expires_at timestamptz,

    -- THE REFRESH LEASE, and it is the single most load-bearing column here.
    --
    -- A Zalo refresh token is SINGLE-USE: spending it kills it and issues a
    -- replacement, so two renewals racing means one of them presents a token
    -- that is already dead and concludes the connection is broken when it is
    -- not. Claiming the lease is an atomic compare-and-set on this row, so
    -- exactly one caller ever reaches the token endpoint.
    --
    -- It is a LEASE rather than a flag because the claimant can die: a process
    -- that rotated and never wrote the result would otherwise hold the renewal
    -- shut forever. After the lease expires another caller may try, and it will
    -- present the old token — which, if the dead claimant did rotate, Zalo
    -- refuses, and the connection parks at reauth_required saying exactly that.
    -- Recovering by asking a human is the only honest end to a rotation whose
    -- outcome nobody kept.
    refresh_claimed_at timestamptz,

    -- THE CURSOR, over message TIME rather than a sequence: Zalo pages this walk
    -- by offset and offsets shift as messages arrive, so a position is not a
    -- resumable identity and a timestamp is. All three are epoch milliseconds,
    -- as the provider reports them.
    --
    -- high_water_mark is the FLOOR: every message at or before it has been
    -- decided about, and nothing looks under it again.
    high_water_mark bigint      NOT NULL DEFAULT 0 CHECK (high_water_mark >= 0),
    -- backfill_before is where an unread region resumes, NULL for none. While it
    -- is set the floor does not move — moving it would put the floor above
    -- messages nothing has read, and no later walk would go under it.
    backfill_before bigint      CHECK (backfill_before IS NULL OR backfill_before > 0),
    -- pending_high_water_mark is the newest message decided about ABOVE an unread
    -- region, NULL when there is none. It is what lets each tick read the newest
    -- messages first while a backlog is still being filled in, and it becomes the
    -- floor the moment the gap closes.
    pending_high_water_mark bigint CHECK (pending_high_water_mark IS NULL OR pending_high_water_mark > 0),
    -- backfill_offset is where in the global walk the unread region STARTED when
    -- the walk ran out of budget. It is a hint and not an identity: the resume
    -- shifts it by what the forward walk has landed since and deliberately steps
    -- one page further back than it needs to, because re-reading a page costs a
    -- deduplicated no-op and skipping one costs the messages in it.
    backfill_offset integer     NOT NULL DEFAULT 0 CHECK (backfill_offset >= 0),

    -- What the last poll did, for the screen and for a human debugging one.
    -- last_error_class is a CLASS, never the provider's own message: it is
    -- rendered, and a provider's error text is a remote party's copy.
    last_polled_at  timestamptz,
    last_error_class text,

    -- Optimistic concurrency for the row, incremented by every update. Two
    -- writers here are ordinary: an admin disconnecting while the poll is
    -- advancing the cursor.
    version         integer     NOT NULL CHECK (version > 0) DEFAULT 1,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),

    -- ONE Official Account per installation, and the constraint is the setup
    -- model rather than a limit discovered later: one OA, one credential, every
    -- rep replying through it. Two rows would be two cursors over two accounts
    -- with one provider namespace between them, where a person captured from
    -- either would collide on the other's ids.
    CONSTRAINT ext_zalo_oa_connection_one_per_workspace UNIQUE (workspace_id)
);

-- ENABLE and FORCE, both. The owner at runtime is margince_owner, and ENABLE
-- alone exempts a table's owner from its own policies — so without FORCE the
-- isolation would hold for margince_app and not for the role every migration and
-- every operator psql session arrives as.
ALTER TABLE ext.ext_zalo_oa_connection ENABLE ROW LEVEL SECURITY;
ALTER TABLE ext.ext_zalo_oa_connection FORCE ROW LEVEL SECURITY;

-- Exactly one policy, permissive, ALL commands, to PUBLIC. A second permissive
-- policy ORs with this one and can only widen it, and USING without WITH CHECK
-- would admit writes into another workspace.
CREATE POLICY ext_zalo_oa_connection_tenant_isolation
    ON ext.ext_zalo_oa_connection
    USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
    WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);

-- The app role runs the unit's handlers, under the policy above. TRUNCATE is
-- deliberately absent: it empties every tenant's rows without consulting the
-- policy's USING clause.
GRANT SELECT, INSERT, UPDATE, DELETE ON ext.ext_zalo_oa_connection TO margince_app;
