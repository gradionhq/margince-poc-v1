-- SPDX-License-Identifier: BUSL-1.1
-- SPDX-FileCopyrightText: 2026 Gradion
--
-- WHAT THIS INSTALLATION SENT, so the poll does not capture it back as a second
-- copy of a message the CRM already has.
--
-- The problem is the provider's walk rather than anything this unit chose:
-- `listrecentchat` is GLOBAL and includes `src = 0`, the Official Account's own
-- outbound, so a reply staged through the timeline is read back by the next tick.
-- The core writes the rep's reply as an activity with no provider id on it, so
-- the two cannot meet on a natural key and both land — one real message, two rows.
--
-- The alternative was to stop capturing outbound entirely, and it was rejected:
-- an Official Account is answered from Zalo's own console too, and those replies
-- are exactly the conversation history a CRM exists to hold. Dropping them would
-- make every timeline one-sided to keep one duplicate away.

CREATE TABLE ext.ext_zalo_oa_sent_message (
    -- The tenant claim, and the column the policy below compares. The cascade is
    -- load-bearing: without it, deleting a workspace fails on this unit's rows.
    workspace_id uuid        NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,

    -- WHICH Official Account sent it. Zalo message ids are scoped to the account,
    -- exactly as its user ids are, so a bare id is not an identity — and an
    -- installation that reconnects to a different account must not suppress a
    -- message there because a number matched.
    oa_id        text        NOT NULL CHECK (length(oa_id) > 0),

    -- The provider's own id for the message, as the send returned it. It is the
    -- same value a later read of that message carries, which is the whole reason
    -- this table can work at all.
    message_id   text        NOT NULL CHECK (length(message_id) > 0),

    sent_at      timestamptz NOT NULL DEFAULT now(),

    -- One row per message per account per tenant. A retried delivery that sends
    -- twice records the second id as well, because it IS a second message at the
    -- provider and the poll will read both back.
    PRIMARY KEY (workspace_id, oa_id, message_id)
);

-- The sweep reads by age within a tenant, and the primary key above starts with
-- the wrong column for that. Deleting a workspace also scans this table once per
-- row it removes unless an index begins with the referencing column.
CREATE INDEX ext_zalo_oa_sent_message_by_age
    ON ext.ext_zalo_oa_sent_message (workspace_id, sent_at);

-- ENABLE and FORCE, both. The owner at runtime is margince_owner, and ENABLE
-- alone exempts a table's owner from its own policies — so without FORCE the
-- isolation would hold for margince_app and not for the role every migration and
-- every operator psql session arrives as.
ALTER TABLE ext.ext_zalo_oa_sent_message ENABLE ROW LEVEL SECURITY;
ALTER TABLE ext.ext_zalo_oa_sent_message FORCE ROW LEVEL SECURITY;

-- Exactly one policy, permissive, ALL commands, to PUBLIC. A second permissive
-- policy ORs with this one and can only widen it, and USING without WITH CHECK
-- would admit writes into another workspace.
CREATE POLICY ext_zalo_oa_sent_message_tenant_isolation
    ON ext.ext_zalo_oa_sent_message
    USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
    WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);

-- The app role runs the unit's handlers, under the policy above. TRUNCATE is
-- deliberately absent: it empties every tenant's rows without consulting the
-- policy's USING clause.
GRANT SELECT, INSERT, UPDATE, DELETE ON ext.ext_zalo_oa_sent_message TO margince_app;
