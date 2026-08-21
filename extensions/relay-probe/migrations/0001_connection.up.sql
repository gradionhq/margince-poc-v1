-- SPDX-License-Identifier: BUSL-1.1
-- SPDX-FileCopyrightText: 2026 Gradion
--
-- One row per connected member: where their Relay is, how far the poll has
-- read, and whether the connection still works. It is applied by the pre-merge
-- gate as a restricted ext_relay_probe role and at runtime by
-- margince_owner; what isolates it in production is the FORCE row level
-- security and the workspace-bound policy below, not its ownership
-- (extensions/notes/migrations/0001 carries the long form of that note).
--
-- THE TOKEN IS NOT HERE. A member's personal access token lives in the unit's
-- user-scoped secret namespace, sealed by the installation's custodian. This
-- table records only THAT a member connected and where their cursor is — which
-- is also what makes the row safe for the screen to render.

CREATE TABLE ext.ext_relay_probe_connection (
    id              uuid        NOT NULL DEFAULT gen_random_uuid() PRIMARY KEY,
    -- The tenant claim, and the column the policy below compares. The cascade
    -- is load-bearing: without it, deleting a workspace fails on this unit's
    -- rows, so erasing a tenant would stop at the first installed extension.
    workspace_id    uuid        NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,

    -- WHOSE connection this is: the member whose token produced the records,
    -- and the authority every ingest from this row runs under. Stamped by the
    -- handler from the invocation's Caller and never from the request body —
    -- a user id a client supplies is a user id a client forges, and here it
    -- would forge the consent the ingress port checks.
    --
    -- NO FOREIGN KEY to the core user table: the role this file is applied as
    -- holds REFERENCES (id) on workspace and nothing else on public. The cost
    -- is real and worth stating — nothing deletes this row when the account is
    -- deleted, so a reader must treat the id as one that may no longer
    -- resolve. The poll does: identity refuses a member who is gone, and the
    -- row parks rather than pretending.
    user_id         uuid        NOT NULL,

    -- WHICH Relay. The bundle names one deployment, the product has others,
    -- and a hardcoded host would also make the integration lane require
    -- egress. Stored per connection because it is the member's own workspace
    -- URL that their token authenticates against.
    base_url        text        NOT NULL CHECK (length(base_url) > 0),

    -- Two states, and no more than the poll can honestly distinguish.
    -- `connected` is working; `reauth_required` is a provider 401 — the token
    -- was revoked or expired, and retrying it on a cadence is how an
    -- installation gets itself rate-limited for nothing. A disconnected member
    -- has no row at all (the disconnect deletes it, with the credential), so
    -- there is no third state that means "here but not here".
    status          text        NOT NULL DEFAULT 'connected'
                    CHECK (status IN ('connected', 'reauth_required')),

    -- What the member's own account is called at the provider, for the screen
    -- to render, and the provider workspace their inbox belongs to. Both are
    -- read from the provider at connect and refreshed by the poll; both are
    -- nullable because a connection exists from the moment the token is
    -- deposited, which is before anything has been read back.
    account_label   text,
    provider_workspace_id text,

    -- THE CURSOR, in two halves, which is the same split core capture already
    -- carries: a forward watermark and a backward resume point, disjoint by
    -- construction.
    --
    -- high_water_mark is the highest inbox id this connection has PROCESSED —
    -- landed, replayed, skipped or filtered alike. It advances past everything
    -- decided and never past anything unprocessed.
    --
    -- backfill_before is where a walk that ran out of page budget stopped. It
    -- is NULL when there is no gap; while it is set, the mark does not move,
    -- because moving it would strand every id below the gap permanently — the
    -- next poll's newest page would already be above the mark and nothing
    -- would ever look under it again.
    -- high_water_mark is the FLOOR: everything at or below it has been decided
    -- about, and nothing looks under it again.
    high_water_mark bigint      NOT NULL DEFAULT 0 CHECK (high_water_mark >= 0),
    -- backfill_before is where an unread region resumes, NULL for none. While
    -- it is set the floor does not move — moving it would put the floor above
    -- ids nothing has read, and no later walk would go under it.
    backfill_before bigint      CHECK (backfill_before IS NULL OR backfill_before > 0),
    -- pending_high_water_mark is the highest id decided about ABOVE an unread
    -- region, NULL when there is none. It is what lets each tick read the
    -- newest messages first while a backlog is still being filled in, and it
    -- becomes the floor the moment the gap closes. Without it a truncated walk
    -- forgot everything it had just decided, and a busy account's cursor
    -- crawled while its newest messages waited for the backlog.
    pending_high_water_mark bigint CHECK (pending_high_water_mark IS NULL OR pending_high_water_mark > 0),

    -- What the last poll did, for the screen and for a human debugging one.
    -- last_error_class is a CLASS, never the provider's own message: it is
    -- rendered, and a provider's error text is a remote party's copy.
    last_polled_at  timestamptz,
    last_error_class text,

    -- Optimistic concurrency for the row, incremented by every update. Two
    -- writers here are ordinary: a member clicking disconnect while the poll
    -- is advancing their cursor.
    version         integer     NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),

    -- One connection per member per workspace. Without it a member could
    -- connect twice and have both rows poll the same inbox, which is not a
    -- duplicate-record problem — the capture key makes the second landing a
    -- no-op — but two cursors advancing over one account, each hiding the
    -- other's gaps.
    CONSTRAINT ext_relay_probe_connection_one_per_member
        UNIQUE (workspace_id, user_id)
);

-- ENABLE and FORCE, both. The owner at runtime is margince_owner, and ENABLE
-- alone exempts a table's owner from its own policies — so without FORCE the
-- isolation would hold for margince_app and not for the role every migration
-- and every operator psql session arrives as.
ALTER TABLE ext.ext_relay_probe_connection ENABLE ROW LEVEL SECURITY;
ALTER TABLE ext.ext_relay_probe_connection FORCE ROW LEVEL SECURITY;

-- Exactly one policy, permissive, ALL commands, to PUBLIC. A second permissive
-- policy ORs with this one and can only widen it, and USING without WITH CHECK
-- would admit writes into another workspace.
CREATE POLICY ext_relay_probe_connection_tenant_isolation
    ON ext.ext_relay_probe_connection
    USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
    WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);

-- The app role runs the unit's handlers, under the policy above. TRUNCATE is
-- deliberately absent: it empties every tenant's rows without consulting the
-- policy's USING clause.
GRANT SELECT, INSERT, UPDATE, DELETE ON ext.ext_relay_probe_connection TO margince_app;
