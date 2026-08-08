-- SPDX-License-Identifier: BUSL-1.1
-- SPDX-FileCopyrightText: 2026 Gradion
--
-- crm-demo's one table. It is applied by the migrate role as the restricted
-- ext_crm_demo role, which holds CREATE on the ext schema and nothing on
-- public beyond REFERENCES (id) on workspace — so every line below is the
-- narrowest shape that role can produce, and check-ext-migrations re-reads the
-- resulting catalog to prove it.
--
-- The name is ext_crm_demo_note, not note: the ext schema is shared by every
-- installed unit, so the unit namespace is what keeps two of them from
-- colliding or addressing each other's rows.

CREATE TABLE ext.ext_crm_demo_note (
    id           uuid        NOT NULL DEFAULT gen_random_uuid() PRIMARY KEY,
    -- The tenant claim, and the column the policy below compares. The cascade
    -- is load-bearing rather than tidy: without it, deleting a workspace fails
    -- on this unit's rows, so erasing a tenant would stop at the first
    -- installed extension.
    workspace_id uuid        NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    body         text        NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now()
);

-- ENABLE and FORCE, both. ENABLE alone leaves the table's OWNER — which is
-- this unit's own ext_crm_demo role — reading and writing every workspace's
-- rows, so the isolation would hold for the app and not for the unit.
ALTER TABLE ext.ext_crm_demo_note ENABLE ROW LEVEL SECURITY;
ALTER TABLE ext.ext_crm_demo_note FORCE ROW LEVEL SECURITY;

-- Exactly one policy, permissive, ALL commands, to PUBLIC. A second
-- permissive policy ORs with this one and can only widen it, and USING
-- without WITH CHECK would admit writes into another workspace.
CREATE POLICY ext_crm_demo_note_tenant_isolation ON ext.ext_crm_demo_note
    USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
    WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);

-- The app role runs the unit's handlers, under the policy above. TRUNCATE is
-- deliberately absent: it empties every tenant's rows without consulting the
-- policy's USING clause.
GRANT SELECT, INSERT, UPDATE, DELETE ON ext.ext_crm_demo_note TO margince_app;
