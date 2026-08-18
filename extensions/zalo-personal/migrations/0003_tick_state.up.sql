-- SPDX-License-Identifier: BUSL-1.1
-- SPDX-FileCopyrightText: 2026 Gradion
--
-- WHAT ONE TICK NEEDS TO KNOW ABOUT A MEMBER that the first two migrations did not
-- give it: which messages the CRM itself sent, and when this member is next worth
-- asking.
--
-- ONE VERSION FOR BOTH because they are one change to one thing — how a tick
-- decides what to read and what to keep. Splitting them would leave a database
-- state where the capture knows which echoes are its own but not how often to look,
-- or the reverse, and neither half is useful alone.
--
-- ============================================================================
-- PART ONE: WHAT THE CRM ITSELF SENT AS THIS MEMBER, remembered just long enough
-- that the capture does not read it back as a second copy of a reply the timeline
-- already holds.
--
-- THE PROBLEM AND WHY THE ANSWER IS A TABLE RATHER THAN A FILTER. Zalo delivers
-- this member's OWN outgoing messages back to their own socket as ordinary
-- inbound frames (`uidFrom: "0"`), carrying the same msgId the send returned. Two
-- different things arrive that way and they are indistinguishable by shape:
--
--   * a reply staged through the CRM, which the core already wrote as an
--     activity — capturing it again puts the rep's words on the customer's
--     timeline twice; and
--   * a reply the rep typed in the Zalo app on their PHONE, which nothing in this
--     installation has ever seen.
--
-- The second one is the COMMON case for the team this connector is for, and the
-- consent copy actively tells them to use their phone rather than Zalo Web. So
-- dropping every echo — which is what this unit did before this migration — makes
-- every conversation one-sided: the customer's half on the timeline, the rep's
-- half nowhere, for exactly the usage we recommend. And keeping every echo
-- double-posts every CRM reply. The only thing that separates them is knowing
-- which ids WE sent, which is what this table is.
--
-- SCOPED TO THE MEMBER, not to the Zalo account, unlike the sibling unit's table:
-- there is exactly one connection per member here, so the member IS the account.
-- What that leaves open is a member who connects a DIFFERENT Zalo account and
-- whose old markers could suppress a message in the new one whose id happens to
-- match. That is handled where the same question is already answered for the
-- cursor — connection.go drops this member's markers when the account changes —
-- rather than by widening the key, so there is one place that says what connecting
-- a different account invalidates.

CREATE TABLE ext.ext_zalo_personal_sent_message (
    -- The tenant claim, and the column the policy below compares. The cascade is
    -- load-bearing: without it, deleting a workspace fails on this unit's rows.
    workspace_id        uuid        NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,

    -- WHOSE send it was. Stamped by the handler from the invocation's own member
    -- and never from a request body, exactly as every other user_id in this unit:
    -- a forged one here would let a caller suppress the capture of somebody
    -- else's messages.
    user_id             uuid        NOT NULL,

    -- The provider's own id for the message, as the send returned it. It is the
    -- same value the echo carries back, which is the whole reason this table can
    -- work at all.
    provider_message_id text        NOT NULL CHECK (length(provider_message_id) > 0),

    created_at          timestamptz NOT NULL DEFAULT now(),

    -- One row per message per member per tenant, and the PRIMARY KEY *is* that
    -- uniqueness rather than a separate constraint beside a surrogate id: this
    -- table has no identity of its own to hand out — a row IS the statement "we
    -- sent this id". A retried delivery that transmits twice records the second
    -- id as well, because it IS a second message at the provider and the capture
    -- will read both back.
    PRIMARY KEY (workspace_id, user_id, provider_message_id)
);

-- The sweep reads by AGE within a tenant, and the primary key above starts with
-- the wrong column for that. Deleting a workspace also scans this table once per
-- row it removes unless an index begins with the referencing column.
CREATE INDEX ext_zalo_personal_sent_message_by_age
    ON ext.ext_zalo_personal_sent_message (workspace_id, created_at);

-- ENABLE and FORCE, both. The owner at runtime is margince_owner, and ENABLE
-- alone exempts a table's owner from its own policies — so without FORCE the
-- isolation would hold for margince_app and not for the role every migration and
-- every operator psql session arrives as.
ALTER TABLE ext.ext_zalo_personal_sent_message ENABLE ROW LEVEL SECURITY;
ALTER TABLE ext.ext_zalo_personal_sent_message FORCE ROW LEVEL SECURITY;

-- Exactly one policy, permissive, ALL commands, to PUBLIC. A second permissive
-- policy ORs with this one and can only widen it, and USING without WITH CHECK
-- would admit writes into another workspace.
CREATE POLICY ext_zalo_personal_sent_message_tenant_isolation
    ON ext.ext_zalo_personal_sent_message
    USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
    WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);

-- The app role runs the unit's handlers, under the policy above. TRUNCATE is
-- deliberately absent: it empties every tenant's rows without consulting the
-- policy's USING clause.
GRANT SELECT, INSERT, UPDATE, DELETE ON ext.ext_zalo_personal_sent_message TO margince_app;

-- ============================================================================
-- PART TWO: HOW OFTEN THIS MEMBER IS WORTH ASKING.
--
-- THE COST THE CADENCE IS ACTUALLY ABOUT IS THE HANDSHAKE, not the data. Measured
-- against the real drains: five messages are about 2.3 KB, and even ~150 queued is
-- ~64 KiB — under a megabyte an hour per member at a five-minute cadence. What each
-- tick really costs is one HTTPS login-info call plus a TLS and websocket handshake,
-- per member, paid whether or not anything arrived. So the lever is FEWER TICKS.
--
-- Two facts kill the obvious alternatives, and both were verified in the capture
-- rather than assumed. A message delivered on a live socket IS re-queued — two of
-- them came back in all three later backlog drains — so holding the socket open
-- does not drain the queue. And the queue returns oldest-first, ascending by msgId,
-- so an early exit at the cursor buys nothing: the already-seen messages arrive
-- first and have to be read through to reach anything new.

ALTER TABLE ext.ext_zalo_personal_connection
    -- How many consecutive drains produced nothing new. It is the only input to the
    -- backoff, and it is reset — not decremented — the moment anything lands,
    -- because a conversation that just started moving is the one worth watching.
    ADD COLUMN idle_streak integer NOT NULL DEFAULT 0 CHECK (idle_streak >= 0),

    -- Nothing before this time. NULL means due now, which is what a fresh connect,
    -- a save of the conversation list, and any productive drain all leave behind.
    --
    -- IT IS COMPARED AGAINST THE DATABASE'S OWN now(), and it is written from it
    -- too (`now() + interval`), deliberately: a value written from the application's
    -- clock and compared against the server's is a scheduling bug that appears only
    -- when the two drift, and it appears as a member who is never due.
    --
    -- THE CEILING ON WHAT MAY BE WRITTEN HERE IS A CORRECTNESS BOUND, NOT A TUNING
    -- PREFERENCE, and the argument lives in Go beside the constant that enforces it
    -- (poll.go, maxPollBackoff): a member not polled inside the server's retention
    -- window loses those messages permanently, so the backoff trades provider load
    -- against DATA LOSS. Retention is claimed to be three days and has been measured
    -- once, at about an hour (DESIGN §9.1, issue #1692) — so the cap is derived from
    -- the measurement and not from the claim.
    ADD COLUMN poll_after  timestamptz;

-- The fleet read asks for members who are DUE, longest-waiting first. Without this
-- index that read is a scan of every connection in the installation once per tick —
-- which is the cost the cadence exists to reduce, paid in the database instead.
CREATE INDEX ext_zalo_personal_connection_due
    ON ext.ext_zalo_personal_connection (workspace_id, poll_after, last_polled_at);
